package host

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verirelay/deploy/internal/config"
	"github.com/verirelay/deploy/internal/network"
	"github.com/verirelay/deploy/internal/plan"
)

// fakeRoot builds /dev, /sys and /proc for an SEV-SNP host with 32 GiB.
func fakeRoot(t *testing.T, snp string) string {
	root := t.TempDir()
	for path, content := range map[string]string{
		"dev/kvm": "", "dev/sev": "",
		"sys/module/kvm_amd/parameters/sev_snp": snp + "\n",
		"proc/meminfo":                          "MemTotal:       33554432 kB\nMemFree:        1000 kB\n",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type runner struct {
	qemuHelp string
	active   bool
	calls    []string
}

func (r *runner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	switch {
	case strings.HasSuffix(name, "qemu-system-x86_64"):
		return []byte(r.qemuHelp), nil
	case name == "systemctl" && args[0] == "is-active":
		if r.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit status 3")
	}
	return nil, nil
}

func listen(t *testing.T) net.Listener {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func freePort(t *testing.T) string {
	l := listen(t)
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// privateDir returns a temporary directory that only its owner can write,
// whatever the umask.
func privateDir(t *testing.T) string {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// instance writes the files of an instance directory that Preflight reads.
func instance(t *testing.T, memoryMiB int64) config.Config {
	dir := privateDir(t)
	c := config.Config{
		Path: filepath.Join(dir, "deploy.toml"), Dir: dir, Release: "rel-1",
		Bootstrap: filepath.Join(dir, "bootstrap.json"), QEMU: filepath.Join(dir, "qemu-system-x86_64"), MemoryMiB: memoryMiB,
	}
	releaseDir := filepath.Join(dir, "releases", c.Release)
	if err := os.MkdirAll(releaseDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{c.Path, c.Bootstrap, c.QEMU, filepath.Join(releaseDir, "OVMF.fd"), filepath.Join(releaseDir, "vmlinuz"), filepath.Join(releaseDir, "initrd.img")} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestPreflight(t *testing.T) {
	veriprov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer veriprov.Close()
	kds := httptest.NewServer(http.NotFoundHandler())
	defer kds.Close()
	held := listen(t)
	defer func() { _ = held.Close() }()
	writable := func(t *testing.T, c config.Config) {
		if err := os.Chmod(c.Bootstrap, 0o620); err != nil { //nolint:gosec // G302: the test needs a group-writable file.
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name      string
		snp       string
		qemuHelp  string
		active    bool
		forward   string
		launched  string
		memoryMiB int64
		veriprov  string
		edit      func(*testing.T, config.Config)
		failed    string
	}{
		{"all pass", "Y", "sev-guest\nsev-snp-guest\n", false, freePort(t), "", 16384, veriprov.URL, nil, ""},
		{"snp off", "N", "sev-snp-guest", false, freePort(t), "", 16384, veriprov.URL, nil, "SEV-SNP"},
		{"old qemu", "Y", "sev-guest\n", false, freePort(t), "", 16384, veriprov.URL, nil, "QEMU"},
		{"writable bootstrap", "Y", "sev-snp-guest", false, freePort(t), "", 16384, veriprov.URL, writable, "Files,QEMU"},
		{"port taken", "Y", "sev-snp-guest", false, held.Addr().String(), "", 16384, veriprov.URL, nil, "Port"},
		{"port held by own unit", "Y", "sev-snp-guest", true, held.Addr().String(), held.Addr().String(), 16384, veriprov.URL, nil, ""},
		{"own unit on another port", "Y", "sev-snp-guest", true, held.Addr().String(), "127.0.0.1:1", 16384, veriprov.URL, nil, "Port"},
		{"memory", "Y", "sev-snp-guest", false, freePort(t), "", 65536, veriprov.URL, nil, "Memory"},
		{"veriprov down", "Y", "sev-snp-guest", false, freePort(t), "", 16384, veriprov.URL + "/down", nil, "VeriProv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &runner{qemuHelp: tc.qemuHelp, active: tc.active}
			h := Host{
				Root: fakeRoot(t, tc.snp), Run: r.run, HTTP: network.Client(false), KDS: kds.URL,
				TrustedUID: uint32(os.Getuid()), //nolint:gosec // G115: uids fit in uint32.
			}
			c := instance(t, tc.memoryMiB)
			if tc.edit != nil {
				tc.edit(t, c)
			}
			p := plan.Plan{Instance: "relay", Forward: tc.forward, VeriProv: tc.veriprov, ConfigDisk: filepath.Join(c.Dir, "config.img")}
			var failed []string
			for _, check := range h.Preflight(t.Context(), c, p, plan.Plan{Forward: tc.launched}) {
				if check.Err != nil {
					failed = append(failed, check.Name)
				}
			}
			if strings.Join(failed, ",") != tc.failed {
				t.Fatalf("failed %v, want %q", failed, tc.failed)
			}
		})
	}
}

func TestInstallUnit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, plan.UnitDir), 0o750); err != nil {
		t.Fatal(err)
	}
	h := Host{Root: root}
	p := plan.Plan{UnitPath: plan.UnitDir + "/verirelay-relay.service", Unit: "[Unit]\n"}
	for i, want := range []bool{true, false} {
		changed, err := h.InstallUnit(p)
		if err != nil || changed != want {
			t.Fatalf("install %d: changed %v, %v", i, changed, err)
		}
	}
	got, err := h.InstalledUnit(p)
	if err != nil || got != p.Unit {
		t.Fatalf("installed %q, %v", got, err)
	}
}
