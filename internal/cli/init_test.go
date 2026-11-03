package cli

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verirelay/protocol/guest"

	"github.com/verirelay/deploy/internal/config"
	"github.com/verirelay/deploy/internal/fake"
	"github.com/verirelay/deploy/internal/host"
	"github.com/verirelay/deploy/internal/network"
)

type initEnv struct {
	app      *App
	out, err strings.Builder
	config   string
	veriprov string
}

// newInitEnv serves the latest release like GitHub: /releases/latest
// redirects to the tag, or answers 404 when latest is empty. Its VeriProv
// serves one Bootstrap per profile.
func newInitEnv(t *testing.T, latest, input string, profiles ...string) *initEnv {
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" || latest == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)
	}))
	t.Cleanup(repo.Close)
	vp := &fake.VeriProv{}
	veriprov := httptest.NewTLSServer(vp)
	t.Cleanup(veriprov.Close)
	for _, p := range profiles {
		vp.Bootstraps = append(vp.Bootstraps, fake.BootstrapFor(veriprov.URL, p))
	}
	e := &initEnv{config: filepath.Join(t.TempDir(), "relay-a", "deploy.toml"), veriprov: veriprov.URL}
	if err := os.MkdirAll(filepath.Dir(e.config), 0o750); err != nil {
		t.Fatal(err)
	}
	input = strings.ReplaceAll(input, "VERIPROV", veriprov.URL)
	e.app = &App{
		In: strings.NewReader(input), Out: &e.out, Err: &e.err,
		HTTP: network.Client(false), Host: host.Host{HTTP: veriprov.Client()}, Releases: repo.URL,
	}
	return e
}

func (e *initEnv) expect(t *testing.T, code int, args ...string) {
	t.Helper()
	if got := e.app.Run(t.Context(), append([]string{"init", "-c", e.config}, args...)); got != code {
		t.Fatalf("verideploy init %v = %d, want %d\nstdout:\n%s\nstderr:\n%s", args, got, code, e.out.String(), e.err.String())
	}
}

func (e *initEnv) contains(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(e.out.String(), w) {
			t.Fatalf("output lacks %q:\n%s", w, e.out.String())
		}
	}
}

func TestInitTemplate(t *testing.T) {
	e := newInitEnv(t, tag, "")
	e.expect(t, 0, "--template")
	e.contains(t, "Fill in veriprov")
	data, err := os.ReadFile(e.config)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`release = "` + tag + `"`, "the latest when this file was written", `veriprov = ""`, `profile = ""`, "# registry = "} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("template lacks %q:\n%s", want, data)
		}
	}
	// Filling in veriprov is all a template needs.
	filled := strings.Replace(string(data), `veriprov = ""`, `veriprov = "https://veriprov.verirelay.com"`, 1)
	c, err := config.Parse([]byte(filled), filepath.Dir(e.config))
	if err != nil || c.Instance != "relay-a" || c.Release != tag || c.VCPUs != 8 || c.Memory != "16G" {
		t.Fatalf("%+v, %v", c, err)
	}
	e.expect(t, 1, "--template")
	if !strings.Contains(e.err.String(), "already exists") {
		t.Fatal(e.err.String())
	}
}

func TestInitTemplateOffline(t *testing.T) {
	e := newInitEnv(t, "", "")
	e.expect(t, 0, "--template")
	data, err := os.ReadFile(e.config)
	if err != nil || !strings.Contains(string(data), "release = \"\"") {
		t.Fatalf("%v\n%s", err, data)
	}
	e.contains(t, "Fill in veriprov and release")
}

func TestInitInteractive(t *testing.T) {
	// Accept the default instance name and the latest release; give an
	// address that is no origin, then the VeriProv, whose one Bootstrap is
	// taken; give an invalid vCPU count, then 4; set the memory; accept the
	// default port.
	e := newInitEnv(t, tag, "\n\nVERIPROV/v1\nVERIPROV\nmany\n4\n8G\n\n", "")
	e.expect(t, 0)
	hostData := guest.HostData(fake.BootstrapFor(e.veriprov, ""))
	e.contains(t,
		"Instance name [relay-a]: ",
		"Release ["+tag+", the latest]: ",
		"must be an HTTPS origin",
		`Bootstrap with profile "", HOST_DATA `+hex.EncodeToString(hostData[:]),
		"vCPU count must be a positive whole number",
		"Next: sudo verideploy fetch, then verideploy plan.",
	)
	c, err := config.Load(e.config)
	if err != nil {
		t.Fatal(err)
	}
	if c.Instance != "relay-a" || c.Release != tag || c.VeriProv != e.veriprov || c.Profile != "" || c.VCPUs != 4 || c.Memory != "8G" {
		t.Fatalf("%+v", c)
	}
}

func TestInitProfiles(t *testing.T) {
	// The VeriProv serves two profiles: an unknown one is asked again.
	e := newInitEnv(t, tag, "\n\nVERIPROV\nc\nb\n\n\n\n", "a", "b")
	e.expect(t, 0)
	e.contains(t, `Bootstrap profile, one of "a", "b" ["a"]: `, `no Bootstrap with profile "c"`, `Bootstrap with profile "b", HOST_DATA `)
	c, err := config.Load(e.config)
	if err != nil || c.Profile != "b" {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestInitEmptyProfileDefault(t *testing.T) {
	// The empty profile is the default and shows as [""]; Enter keeps it.
	e := newInitEnv(t, tag, "\n\nVERIPROV\n\n\n\n\n", "", "test")
	e.expect(t, 0)
	e.contains(t, `Bootstrap profile, one of "", "test" [""]: `, `Bootstrap with profile "", HOST_DATA `)
	if c, err := config.Load(e.config); err != nil || c.Profile != "" {
		t.Fatalf("%+v, %v", c, err)
	}
	// Typing "" picks the empty profile when another is the default.
	e = newInitEnv(t, tag, "\n\nVERIPROV\n\"\"\n\n\n\n", "a", "")
	e.expect(t, 0)
	if c, err := config.Load(e.config); err != nil || c.Profile != "" {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestInitVeriProvUnreachable(t *testing.T) {
	// A VeriProv that cannot be reached is kept with a warning.
	e := newInitEnv(t, tag, "\n\nhttps://127.0.0.1:1\n\n\n\n\n")
	e.expect(t, 0)
	e.contains(t, "verideploy fetch will try again", `Bootstrap profile [""]: `)
	c, err := config.Load(e.config)
	if err != nil || c.VeriProv != "https://127.0.0.1:1" {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestInitInputEnds(t *testing.T) {
	e := newInitEnv(t, tag, "relay\n")
	e.expect(t, 1)
	if _, err := os.Stat(e.config); !os.IsNotExist(err) || !strings.Contains(e.err.String(), "nothing written") {
		t.Fatalf("%v %s", err, e.err.String())
	}
}
