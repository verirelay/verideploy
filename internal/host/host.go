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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/verirelay/verideploy/internal/atomic"
	"github.com/verirelay/verideploy/internal/config"
	"github.com/verirelay/verideploy/internal/network"
	"github.com/verirelay/verideploy/internal/plan"
)

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs a command directly, without a shell.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 -- name and args are fixed commands or the validated QEMU path; no shell.
}

// Host is the machine VeriDeploy runs on. Tests point Root at a fake tree and
// replace Run, HTTP and KDS.
type Host struct {
	Root string
	Run  Runner
	HTTP *http.Client
	KDS  string
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
	checks := []Check{h.sevSNP(), h.qemu(ctx, c.QEMU)}
	for _, forward := range p.Forward {
		checks = append(checks, h.port(ctx, p, launched, forward))
	}
	return append(checks, h.memory(c.MemoryMiB), h.veriprov(ctx, p.VeriProv), h.kds(ctx))
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

func (h Host) port(ctx context.Context, p, launched plan.Plan, forward string) Check {
	c := Check{Name: "Port", Detail: forward + " is free"}
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", forward)
	if err == nil {
		_ = l.Close()
		return c
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		c.Err = err
		return c
	}
	unit := plan.UnitName(p.Instance)
	if slices.Contains(launched.Forward, forward) && launched.UserUnit == p.UserUnit && h.Active(ctx, p.UserUnit, unit) {
		c.Detail = forward + " is held by " + unit
		return c
	}
	c.Err = fmt.Errorf("%s is in use by another process", forward)
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

// manager prefixes args with --user for the user's own systemd manager.
func manager(user bool, args ...string) []string {
	if user {
		return append([]string{"--user"}, args...)
	}
	return args
}

// Active reports whether unit is active.
func (h Host) Active(ctx context.Context, user bool, unit string) bool {
	_, err := h.Run(ctx, "systemctl", manager(user, "is-active", "--quiet", unit)...)
	return err == nil
}

// State returns the ActiveState of unit, such as active or failed.
func (h Host) State(ctx context.Context, user bool, unit string) string {
	out, _ := h.Run(ctx, "systemctl", manager(user, "is-active", unit)...)
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return "unknown"
}

// Systemctl runs systemctl with args.
func (h Host) Systemctl(ctx context.Context, user bool, args ...string) error {
	args = manager(user, args...)
	out, err := h.Run(ctx, "systemctl", args...)
	if err == nil {
		return nil
	}
	err = fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	if user && bytes.Contains(out, []byte("Failed to connect to")) {
		err = fmt.Errorf("%w\nThe user's systemd manager cannot be reached. It runs while the user is logged in, and always once lingering is on (loginctl enable-linger)", err)
	}
	return err
}

// RuntimeDir returns XDG_RUNTIME_DIR for the user's systemd manager when it
// is unset: /run/user/<uid>, where systemd keeps it while that manager runs.
// sudo -u USER -i and su open no login session, so they leave it unset.
func (h Host) RuntimeDir(uid int) string {
	dir := h.path(fmt.Sprintf("/run/user/%d", uid))
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return fmt.Sprintf("/run/user/%d", uid)
}

// Journal returns the messages of unit since since, one per line.
func (h Host) Journal(ctx context.Context, user bool, unit string, since time.Time) ([]byte, error) {
	return h.Run(ctx, "journalctl", manager(user, "--unit", unit, "--since", "@"+strconv.FormatInt(since.Unix(), 10), "--output", "cat", "--no-pager")...)
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
	// A user's unit directory may not exist yet.
	if err := os.MkdirAll(filepath.Dir(h.path(p.UnitPath)), 0o755); err != nil { //nolint:gosec // G301: unit directories are world-readable.
		return false, err
	}
	return true, atomic.WriteFile(h.path(p.UnitPath), []byte(p.Unit), 0o644)
}
