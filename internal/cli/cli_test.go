package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/verirelay/deploy/internal/fake"
	"github.com/verirelay/deploy/internal/host"
	"github.com/verirelay/deploy/internal/network"
	"github.com/verirelay/deploy/internal/plan"
)

const tag = "rel-2026.10.30-1"

// runner plays systemd and QEMU: restarting the unit starts the fake CVM on
// the forward address, as QEMU's port forward would.
type runner struct {
	active  bool
	journal string
	// fail makes the next call with this prefix fail.
	fail    string
	calls   []string
	restart func()
}

func (r *runner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := filepath.Base(name) + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.fail != "" && strings.HasPrefix(call, r.fail) {
		r.fail = ""
		return []byte("failed"), errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(call, "qemu-system-x86_64 -object help"):
		return []byte("sev-guest\nsev-snp-guest\n"), nil
	case strings.HasPrefix(call, "systemctl is-active"):
		if r.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit status 3")
	case strings.HasPrefix(call, "systemctl restart"):
		r.restart()
		r.active = true
	case strings.HasPrefix(call, "journalctl"):
		return []byte(r.journal), nil
	}
	return nil, nil
}

func (r *runner) systemctl() []string {
	var out []string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "systemctl ") && !strings.HasPrefix(c, "systemctl is-active") {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	t         *testing.T
	dir, root string
	forward   string
	bootstrap []byte
	runner    *runner
	app       *App
	out, err  strings.Builder
}

func freeAddr(t *testing.T) string {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// newEnv builds an instance whose CVM, once started, is served by cvm, with
// a VeriProv that is ready, its Bootstrap already fetched, and a release
// signed by a test key.
func newEnv(t *testing.T, cvm *fake.CVM) *env {
	vp := &fake.VeriProv{}
	veriprov := httptest.NewTLSServer(vp)
	t.Cleanup(veriprov.Close)
	e := &env{t: t, dir: t.TempDir(), root: t.TempDir(), forward: freeAddr(t), bootstrap: fake.BootstrapFor(veriprov.URL, "")}
	vp.Bootstraps = [][]byte{e.bootstrap}
	// Only the owner may write the instance directory, whatever the umask.
	if err := os.Chmod(e.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if cvm.Bootstrap == nil {
		cvm.Bootstrap = e.bootstrap
	}
	e.runner = &runner{restart: func() {
		if e.runner.active {
			return
		}
		l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", e.forward)
		if err != nil {
			t.Fatal(err)
		}
		server := &httptest.Server{Listener: l, Config: &http.Server{Handler: cvm, ReadHeaderTimeout: time.Second}}
		server.Start()
		t.Cleanup(server.Close)
	}}

	key := fake.Key(1)
	fake.WriteRelease(t, filepath.Join(e.dir, "releases", tag), tag, key, 1, 8)
	fake.WriteFile(t, filepath.Join(e.dir, "bootstrap.json"), e.bootstrap)
	qemu := filepath.Join(e.dir, "qemu-system-x86_64")
	fake.WriteFile(t, qemu, nil)
	pub := base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	e.writeConfig("1G", pub, qemu, veriprov.URL)

	for path, content := range map[string]string{
		"dev/kvm": "", "dev/sev": "",
		"sys/module/kvm_amd/parameters/sev_snp": "Y\n",
		"proc/meminfo":                          "MemTotal:       33554432 kB\n",
	} {
		full := filepath.Join(e.root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		fake.WriteFile(t, full, []byte(content))
	}
	if err := os.MkdirAll(filepath.Join(e.root, plan.UnitDir), 0o750); err != nil {
		t.Fatal(err)
	}
	e.app = &App{
		Out: &e.out, Err: &e.err,
		Host: host.Host{
			Root: e.root, Run: e.runner.run, HTTP: veriprov.Client(), KDS: veriprov.URL,
			TrustedUID: uint32(os.Getuid()), //nolint:gosec // G115: uids fit in uint32.
		},
		Download:     network.Client(true),
		HTTP:         network.Client(false),
		Euid:         func() int { return 0 },
		PollInterval: 10 * time.Millisecond,
	}
	return e
}

func (e *env) writeConfig(memory, key, qemu, veriprov string) {
	fake.WriteFile(e.t, filepath.Join(e.dir, "deploy.toml"), fmt.Appendf(nil, `version = "verirelay-deploy-config/1"
instance = "relay"
release = %q
veriprov = %q
qemu = %q
release_keys = [%q]
registry = ""

[machine]
vcpus = 8
memory = %q

[network]
forward = %q
`, tag, veriprov, qemu, key, memory, e.forward))
}

func (e *env) editConfig(from, to string) {
	path := filepath.Join(e.dir, "deploy.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatal(err)
	}
	fake.WriteFile(e.t, path, []byte(strings.Replace(string(data), from, to, 1)))
}

func (e *env) run(args ...string) int {
	e.out.Reset()
	e.err.Reset()
	e.runner.calls = nil
	return e.app.Run(e.t.Context(), append(args, "-c", filepath.Join(e.dir, "deploy.toml")))
}

func (e *env) expect(code int, args ...string) {
	e.t.Helper()
	if got := e.run(args...); got != code {
		e.t.Fatalf("deploy %s = %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), got, code, e.out.String(), e.err.String())
	}
}

const restartUnit = "systemctl restart verirelay-relay.service"

func TestUpAndStatus(t *testing.T) {
	e := newEnv(t, &fake.CVM{Ready: true, Measurement: fake.Measurement(8)})
	e.expect(0, "plan")
	for _, want := range []string{"not installed. up writes:", "\n  ExecStart=", "\n    -enable-kvm", "\n\n  [Service]", "accept measurement " + fake.Measurement(8)} {
		if !strings.Contains(e.out.String(), want) {
			t.Fatalf("plan output lacks %q:\n%s", want, e.out.String())
		}
	}
	if len(e.runner.systemctl()) != 0 {
		t.Fatalf("plan ran %v", e.runner.systemctl())
	}

	e.expect(0, "up")
	want := []string{"systemctl daemon-reload", "systemctl enable verirelay-relay.service", restartUnit}
	if got := e.runner.systemctl(); !slices.Equal(got, want) {
		t.Fatalf("systemctl %v, want %v", got, want)
	}
	unit, err := os.ReadFile(filepath.Join(e.root, plan.UnitDir, "verirelay-relay.service"))
	if err != nil || !strings.Contains(string(unit), "-smp 8 -m 1G") {
		t.Fatalf("unit %v:\n%s", err, unit)
	}
	bootstrap, err := os.ReadFile(filepath.Join(e.dir, "bootstrap.json"))
	if err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(filepath.Join(e.dir, "config.img"))
	if err != nil || len(disk) != plan.ConfigDiskSize || !strings.HasPrefix(string(disk), string(bootstrap)+"\x00") {
		t.Fatalf("config.img: %d bytes, %v", len(disk), err)
	}

	// The CVM now holds the port and runs the plan: nothing to restart.
	e.expect(0, "up")
	if slices.Contains(e.runner.systemctl(), restartUnit) {
		t.Fatalf("restarted a CVM that runs the plan: %v", e.runner.systemctl())
	}
	e.expect(0, "plan")
	if !strings.Contains(e.out.String(), "unchanged") || !strings.Contains(e.out.String(), "held by verirelay-relay.service") {
		t.Fatal(e.out.String())
	}

	e.expect(0, "status")
	if strings.Contains(e.out.String(), "FAIL") {
		t.Fatal(e.out.String())
	}
	// Preparing a new Bootstrap does not change what status expects.
	fake.WriteFile(t, filepath.Join(e.dir, "bootstrap.json"), append(bootstrap, '\n'))
	e.expect(0, "status")
}

// TestUpRestartsAfterFailedUp: an up that wrote the unit but failed before
// restarting leaves plan.json behind, so the next up still restarts.
func TestUpRestartsAfterFailedUp(t *testing.T) {
	e := newEnv(t, &fake.CVM{Ready: true, Measurement: fake.Measurement(8)})
	e.expect(0, "up")
	e.editConfig(`memory = "1G"`, `memory = "2G"`)
	e.runner.fail = "systemctl daemon-reload"
	e.expect(1, "up")
	e.expect(0, "up")
	if !slices.Contains(e.runner.systemctl(), restartUnit) {
		t.Fatalf("did not restart: %v", e.runner.systemctl())
	}
}

func TestStatusMismatch(t *testing.T) {
	// The CVM runs another measurement than up launched.
	e := newEnv(t, &fake.CVM{Ready: true, Measurement: fake.Measurement(1)})
	e.expect(0, "up")
	e.expect(1, "status")
	if !strings.Contains(e.out.String(), "FAIL  measurement "+fake.Measurement(1)) {
		t.Fatal(e.out.String())
	}
}

func TestUpNotReady(t *testing.T) {
	e := newEnv(t, &fake.CVM{Measurement: fake.Measurement(8)})
	e.runner.journal = `{"msg":"startup failed; retrying","err":"provisioning unavailable: POST /v1/provision: HTTP 409 (policy.denied)"}`
	e.expect(1, "up", "--timeout", "100ms")
	if !strings.Contains(e.err.String(), "lacks measurement "+fake.Measurement(8)) {
		t.Fatal(e.err.String())
	}
}

func TestUpRefusesFailedChecks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*env)
		want  string
	}{
		{"port taken", func(e *env) {
			l, err := (&net.ListenConfig{}).Listen(e.t.Context(), "tcp", e.forward)
			if err != nil {
				e.t.Fatal(err)
			}
			e.t.Cleanup(func() { _ = l.Close() })
		}, "in use by another process"},
		{"unit of another instance", func(e *env) {
			fake.WriteFile(e.t, filepath.Join(e.root, plan.UnitDir, "verirelay-relay.service"), []byte("# Generated by deploy from /srv/other/deploy.toml.\n"))
		}, "belongs to another config file"},
		{"writable config", func(e *env) {
			if err := os.Chmod(filepath.Join(e.dir, "deploy.toml"), 0o666); err != nil { //nolint:gosec // G302: the test needs a writable file.
				e.t.Fatal(err)
			}
		}, "writable by group or others"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, &fake.CVM{Ready: true})
			tc.setup(e)
			e.expect(1, "plan")
			if !strings.Contains(e.out.String(), tc.want) {
				t.Fatal(e.out.String())
			}
			e.expect(1, "up")
			if !strings.Contains(e.err.String(), "Nothing changed") || len(e.runner.systemctl()) != 0 {
				t.Fatalf("%s %v", e.err.String(), e.runner.systemctl())
			}
			if _, err := os.Stat(filepath.Join(e.dir, "config.img")); !os.IsNotExist(err) {
				t.Fatal("wrote config.img")
			}
		})
	}
}

func TestUsage(t *testing.T) {
	e := newEnv(t, &fake.CVM{Ready: true})
	e.app.Euid = func() int { return 1000 }
	e.expect(2, "up")
	e.expect(2, "down")
	e.expect(2, "launch")
	e.expect(2, "status", "extra")
	e.expect(0, "status", "-h")
	e.expect(1, "status")
}

func TestFetch(t *testing.T) {
	e := newEnv(t, &fake.CVM{Ready: true})
	src := filepath.Join(e.dir, "releases", tag)
	served := t.TempDir()
	for _, name := range []string{"OVMF.fd", "vmlinuz", "initrd.img", "SHA256SUMS", "chain.json"} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		fake.WriteFile(t, filepath.Join(served, name), data)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	repo := httptest.NewServer(http.StripPrefix("/releases/download/"+tag+"/", http.FileServer(http.Dir(served))))
	defer repo.Close()
	// A Build Record must name the repository the release comes from.
	record := fake.Sign(fake.Key(1), tag, fake.Measurement(8))
	record.Statement.Repository = repo.URL
	fake.WriteJSON(t, filepath.Join(served, "build-records.json"), []fake.Record{record.Resign(fake.Key(1))})
	e.editConfig("[machine]", fmt.Sprintf("release_repository = %q\n\n[machine]", repo.URL))
	if err := os.Remove(filepath.Join(e.dir, "bootstrap.json")); err != nil {
		t.Fatal(err)
	}
	e.expect(1, "plan")
	if !strings.Contains(e.err.String(), "Run deploy fetch first") {
		t.Fatal(e.err.String())
	}
	e.expect(0, "fetch")
	if !strings.Contains(e.out.String(), "Verified "+tag) || !strings.Contains(e.out.String(), "Bootstrap from https://") || !strings.Contains(e.out.String(), " saved: HOST_DATA ") {
		t.Fatal(e.out.String())
	}
	got, err := os.ReadFile(filepath.Join(e.dir, "bootstrap.json"))
	if err != nil || string(got) != string(e.bootstrap) {
		t.Fatalf("bootstrap.json %q, %v", got, err)
	}
	e.expect(0, "fetch")
	if !strings.Contains(e.out.String(), "unchanged: HOST_DATA") {
		t.Fatal(e.out.String())
	}
	e.expect(0, "plan")

	// A profile the VeriProv does not serve fails fetch.
	e.editConfig("[machine]", "profile = \"other\"\n\n[machine]")
	e.expect(1, "fetch")
	if !strings.Contains(e.err.String(), `no Bootstrap with profile "other"`) {
		t.Fatal(e.err.String())
	}
}
