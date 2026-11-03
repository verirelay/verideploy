package host

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/verirelay/verideploy/internal/config"
	"github.com/verirelay/verideploy/internal/network"
	"github.com/verirelay/verideploy/internal/plan"
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
	case name == "systemctl" && slices.Contains(args, "is-active"):
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
	for _, tc := range []struct {
		name      string
		snp       string
		qemuHelp  string
		active    bool
		forward   string
		launched  plan.Plan
		memoryMiB int64
		veriprov  string
		failed    string
	}{
		{"all pass", "Y", "sev-guest\nsev-snp-guest\n", false, freePort(t), plan.Plan{}, 16384, veriprov.URL, ""},
		{"snp off", "N", "sev-snp-guest", false, freePort(t), plan.Plan{}, 16384, veriprov.URL, "SEV-SNP"},
		{"old qemu", "Y", "sev-guest\n", false, freePort(t), plan.Plan{}, 16384, veriprov.URL, "QEMU"},
		{"port taken", "Y", "sev-snp-guest", false, held.Addr().String(), plan.Plan{}, 16384, veriprov.URL, "Port"},
		{"port held by own unit", "Y", "sev-snp-guest", true, held.Addr().String(), plan.Plan{Forward: held.Addr().String()}, 16384, veriprov.URL, ""},
		{"own unit on another port", "Y", "sev-snp-guest", true, held.Addr().String(), plan.Plan{Forward: "127.0.0.1:1"}, 16384, veriprov.URL, "Port"},
		{"own unit in the other manager", "Y", "sev-snp-guest", true, held.Addr().String(), plan.Plan{Forward: held.Addr().String(), UserUnit: true}, 16384, veriprov.URL, "Port"},
		{"memory", "Y", "sev-snp-guest", false, freePort(t), plan.Plan{}, 65536, veriprov.URL, "Memory"},
		{"veriprov down", "Y", "sev-snp-guest", false, freePort(t), plan.Plan{}, 16384, veriprov.URL + "/down", "VeriProv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &runner{qemuHelp: tc.qemuHelp, active: tc.active}
			h := Host{Root: fakeRoot(t, tc.snp), Run: r.run, HTTP: network.Client(false), KDS: kds.URL}
			c := config.Config{QEMU: config.DefaultQEMU, MemoryMiB: tc.memoryMiB}
			p := plan.Plan{Instance: "relay", Forward: tc.forward, VeriProv: tc.veriprov}
			var failed []string
			for _, check := range h.Preflight(t.Context(), c, p, tc.launched) {
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
	// A user's unit directory need not exist yet.
	h := Host{Root: t.TempDir()}
	p := plan.Plan{UnitPath: "/home/ops/.config/systemd/user/verirelay-relay.service", Unit: "[Unit]\n"}
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

func TestRuntimeDir(t *testing.T) {
	root := t.TempDir()
	h := Host{Root: root}
	if got := h.RuntimeDir(1000); got != "" {
		t.Fatalf("no manager running: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "run/user/1000"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := h.RuntimeDir(1000); got != "/run/user/1000" {
		t.Fatalf("got %q", got)
	}
}

func TestUnreachableUserManager(t *testing.T) {
	h := Host{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Failed to connect to user scope bus via local transport: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined"), errors.New("exit status 1")
	}}
	err := h.Systemctl(t.Context(), true, "daemon-reload")
	if err == nil || !strings.Contains(err.Error(), "loginctl enable-linger") {
		t.Fatalf("%v", err)
	}
	if err := h.Systemctl(t.Context(), false, "daemon-reload"); err == nil || strings.Contains(err.Error(), "linger") {
		t.Fatalf("system manager error: %v", err)
	}
}

func TestUserManager(t *testing.T) {
	r := &runner{active: true}
	h := Host{Run: r.run}
	if !h.Active(t.Context(), true, "verirelay-relay.service") {
		t.Fatal("not active")
	}
	if err := h.Systemctl(t.Context(), true, "restart", "verirelay-relay.service"); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user is-active --quiet verirelay-relay.service", "systemctl --user restart verirelay-relay.service"}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %q", r.calls)
	}
}
