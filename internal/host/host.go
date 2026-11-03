// Package host inspects and changes the SEV-SNP host: preflight checks, the
// systemd unit and the journal.
package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/verirelay/deploy/internal/atomic"
	"github.com/verirelay/deploy/internal/config"
	"github.com/verirelay/deploy/internal/network"
	"github.com/verirelay/deploy/internal/plan"
	"github.com/verirelay/deploy/internal/release"
)

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs a command directly, without a shell.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 -- name and args are fixed commands or the validated QEMU path; no shell.
}

// Host is the machine Deploy runs on. Tests point Root at a fake tree, add
// their own uid as TrustedUID, and replace Run, HTTP and KDS.
type Host struct {
	Root string
	Run  Runner
	HTTP *http.Client
	KDS  string
	// TrustedUID may own the files root uses, besides root itself.
	TrustedUID uint32
}

// New returns the real host.
func New() Host {
	return Host{Root: "/", Run: Exec, HTTP: network.Client(false), KDS: "https://kdsintf.amd.com"}
}

func (h Host) path(p string) string { return filepath.Join(h.Root, p) }

// Check is one preflight result; Err is nil when it passed.
type Check struct {
	Name   string
	Detail string
	Err    error
}

// Preflight checks that the host can launch p. launched is the Plan up last
// launched, zero when there is none.
func (h Host) Preflight(ctx context.Context, c config.Config, p plan.Plan, launched plan.Plan) []Check {
	files := h.files(c, p)
	qemu := Check{Name: "QEMU", Err: errors.New("not run until the Files check passes")}
	if files.Err == nil {
		qemu = h.qemu(ctx, c.QEMU)
	}
	return []Check{
		files,
		h.sevSNP(),
		qemu,
		h.port(ctx, p, launched),
		h.memory(c.MemoryMiB),
		h.veriprov(ctx, p.VeriProv),
		h.kds(ctx),
	}
}

// files checks that only root can change what up reads and what QEMU, run as
// root, boots: otherwise any user who can write there could swap the kernel
// or point config.img at a host file.
func (h Host) files(c config.Config, p plan.Plan) Check {
	check := Check{Name: "Files", Detail: "only root can change the config, the Bootstrap, QEMU and the boot files"}
	paths := []string{c.Path, c.Bootstrap, c.QEMU}
	for _, name := range release.BootFiles {
		paths = append(paths, filepath.Join(c.Dir, "releases", c.Release, name))
	}
	if _, err := os.Lstat(p.ConfigDisk); err == nil {
		paths = append(paths, p.ConfigDisk)
	}
	for _, path := range paths {
		if err := h.rootOnly(path); err != nil {
			check.Err = err
			return check
		}
	}
	return check
}

// rootOnly checks every component of path, and of the path its symlinks
// resolve to, from / down.
func (h Host) rootOnly(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for _, p := range []string{path, resolved} {
		for dir := p; ; dir = filepath.Dir(dir) {
			fi, err := os.Lstat(dir)
			if err != nil {
				return err
			}
			if err := h.trusted(dir, fi); err != nil {
				return err
			}
			if dir == "/" {
				break
			}
		}
	}
	return nil
}

// trusted requires an owner of root or TrustedUID and no write access for
// group or others. A sticky directory such as /tmp is fine: others cannot
// rename what they do not own there.
func (h Host) trusted(path string, fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: no owner information", path)
	}
	if st.Uid != 0 && st.Uid != h.TrustedUID {
		return fmt.Errorf("%s is owned by uid %d, not root", path, st.Uid)
	}
	mode := fi.Mode()
	if mode&fs.ModeSymlink == 0 && mode.Perm()&0o022 != 0 && (!mode.IsDir() || mode&fs.ModeSticky == 0) {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}

func (h Host) sevSNP() Check {
	c := Check{Name: "SEV-SNP", Detail: "/dev/kvm, /dev/sev, kvm_amd sev_snp=Y"}
	for _, dev := range []string{"/dev/kvm", "/dev/sev"} {
		if _, err := os.Stat(h.path(dev)); err != nil {
			c.Err = fmt.Errorf("%s is missing", dev)
			return c
		}
	}
	param, err := os.ReadFile(h.path("/sys/module/kvm_amd/parameters/sev_snp"))
	if err != nil || strings.TrimSpace(string(param)) != "Y" {
		c.Err = errors.New("SEV-SNP is not enabled in kvm_amd")
	}
	return c
}

func (h Host) qemu(ctx context.Context, binary string) Check {
	c := Check{Name: "QEMU", Detail: binary + " supports sev-snp-guest"}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := h.Run(ctx, binary, "-object", "help")
	switch {
	case err != nil:
		c.Err = fmt.Errorf("%s -object help: %w", binary, err)
	case !bytes.Contains(out, []byte("sev-snp-guest")):
		c.Err = fmt.Errorf("%s has no sev-snp-guest object; SEV-SNP needs QEMU 9.1 or later", binary)
	}
	return c
}

func (h Host) port(ctx context.Context, p, launched plan.Plan) Check {
	c := Check{Name: "Port", Detail: p.Forward + " is free"}
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", p.Forward)
	if err == nil {
		_ = l.Close()
		return c
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		c.Err = err
		return c
	}
	unit := plan.UnitName(p.Instance)
	if launched.Forward == p.Forward && h.Active(ctx, unit) {
		c.Detail = p.Forward + " is held by " + unit
		return c
	}
	c.Err = fmt.Errorf("%s is in use by another process", p.Forward)
	return c
}

func (h Host) memory(want int64) Check {
	c := Check{Name: "Memory"}
	total, err := memTotalMiB(h.path("/proc/meminfo"))
	switch {
	case err != nil:
		c.Err = err
	case want > total:
		c.Err = fmt.Errorf("%d MiB requested, the host has %d MiB", want, total)
	default:
		c.Detail = fmt.Sprintf("%d of %d MiB", want, total)
	}
	return c
}

func memTotalMiB(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			return kb / 1024, nil
		}
	}
	return 0, errors.New("no MemTotal in " + path)
}

func (h Host) veriprov(ctx context.Context, origin string) Check {
	c := Check{Name: "VeriProv", Detail: origin + "/readyz answers 200 with this host's CA roots; the CVM checks its own"}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := network.Get(ctx, h.HTTP, origin+"/readyz", 1<<10); err != nil {
		c.Err = fmt.Errorf("%s/readyz: %w", origin, err)
	}
	return c
}

func (h Host) kds(ctx context.Context) Check {
	c := Check{Name: "AMD KDS", Detail: h.KDS + " is reachable"}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.KDS, nil)
	if err == nil {
		var res *http.Response
		if res, err = h.HTTP.Do(req); err == nil {
			_ = res.Body.Close()
		}
	}
	if err != nil {
		c.Err = fmt.Errorf("%s: %w", h.KDS, err)
	}
	return c
}

// Active reports whether unit is active.
func (h Host) Active(ctx context.Context, unit string) bool {
	_, err := h.Run(ctx, "systemctl", "is-active", "--quiet", unit)
	return err == nil
}

// State returns the ActiveState of unit, such as active or failed.
func (h Host) State(ctx context.Context, unit string) string {
	out, _ := h.Run(ctx, "systemctl", "is-active", unit)
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return "unknown"
}

// Systemctl runs systemctl with args.
func (h Host) Systemctl(ctx context.Context, args ...string) error {
	out, err := h.Run(ctx, "systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

// Journal returns the messages of unit since since, one per line.
func (h Host) Journal(ctx context.Context, unit string, since time.Time) ([]byte, error) {
	return h.Run(ctx, "journalctl", "--unit", unit, "--since", "@"+strconv.FormatInt(since.Unix(), 10), "--output", "cat", "--no-pager")
}

// InstalledUnit returns the unit file at p.UnitPath, or "" when there is none.
func (h Host) InstalledUnit(p plan.Plan) (string, error) {
	data, err := os.ReadFile(h.path(p.UnitPath))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// InstallUnit writes p's unit file and reports whether it changed.
func (h Host) InstallUnit(p plan.Plan) (bool, error) {
	installed, err := h.InstalledUnit(p)
	if err != nil || installed == p.Unit {
		return false, err
	}
	return true, atomic.WriteFile(h.path(p.UnitPath), []byte(p.Unit), 0o644)
}
