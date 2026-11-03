package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `version = "verirelay-deploy-config/1"
instance = "relay"
release = "rel-2026.10.30-1"
veriprov = "https://veriprov.verirelay.com"

[machine]
vcpus = 8
memory = "16G"

[network]
forward = "127.0.0.1:18081"
`

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy.toml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dir != dir || c.Bootstrap != filepath.Join(dir, "bootstrap.json") || c.VeriProv != "https://veriprov.verirelay.com" || c.Profile != "" {
		t.Fatalf("paths: %+v", c)
	}
	if c.MemoryMiB != 16384 || c.Forward.String() != "127.0.0.1:18081" || c.VCPUs != 8 {
		t.Fatalf("machine: %+v", c)
	}
	if c.QEMU != DefaultQEMU || c.ReleaseRepository != DefaultReleaseRepository || c.Registry != DefaultRegistry {
		t.Fatalf("defaults: %+v", c)
	}
	if len(c.ReleaseKeys) != 1 {
		t.Fatalf("release keys: %d", len(c.ReleaseKeys))
	}
}

func TestOptionalKeys(t *testing.T) {
	head := `qemu = "/opt/qemu/bin/qemu-system-x86_64"
release_repository = "https://github.com/example/release/"
release_keys = ["c0quXUfgy8ZYKZA-oXVzEDmU75WuaT2xKx5W-3EHqaI", "E0mn567myKlD1eCT3Ux89j0IoSwK_zJ4BdQtBrq7Bbs"]
registry = ""
`
	c, err := Parse([]byte(head+valid), "/srv/relay")
	if err != nil {
		t.Fatal(err)
	}
	if c.QEMU != "/opt/qemu/bin/qemu-system-x86_64" || c.ReleaseRepository != "https://github.com/example/release" || c.Registry != "" || len(c.ReleaseKeys) != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestRejects(t *testing.T) {
	for _, tc := range []struct{ name, from, to, dir, want string }{
		{"unknown key", "[network]", "extra = 1\n[network]", "", "unknown key"},
		{"unknown table key", `memory = "16G"`, "memory = \"16G\"\ncpus = 2", "", "unknown key"},
		{"duplicate key", `vcpus = 8`, "vcpus = 8\nvcpus = 4", "", "vcpus"},
		{"version", `deploy-config/1`, `deploy-config/2`, "", "version"},
		{"instance", `instance = "relay"`, `instance = "Relay"`, "", "instance"},
		{"tag", `release = "rel-2026.10.30-1"`, `release = "../etc"`, "", "release"},
		{"vcpus", `vcpus = 8`, `vcpus = 0`, "", "vcpus"},
		{"memory unit", `memory = "16G"`, `memory = "16GB"`, "", "memory"},
		{"memory size", `memory = "16G"`, `memory = "256M"`, "", "below"},
		{"forward ipv6", `forward = "127.0.0.1:18081"`, `forward = "[::1]:18081"`, "", "forward"},
		{"forward port", `forward = "127.0.0.1:18081"`, `forward = "127.0.0.1:0"`, "", "forward"},
		{"no veriprov", `veriprov = "https://veriprov.verirelay.com"`, `veriprov = ""`, "", "HTTPS origin"},
		{"veriprov path", `veriprov = "https://veriprov.verirelay.com"`, `veriprov = "https://veriprov.verirelay.com/v1"`, "", "HTTPS origin"},
		{"veriprov http", `veriprov = "https://veriprov.verirelay.com"`, `veriprov = "http://veriprov.verirelay.com"`, "", "HTTPS origin"},
		{"profile", "[machine]", "profile = \"a b\"\n[machine]", "", "profile"},
		{"bootstrap file", "[machine]", "bootstrap = \"bootstrap.json\"\n[machine]", "", "unknown key"},
		{"dir comma", "", "", "/srv/a,b", "instance directory"},
		{"qemu relative", "[machine]", "qemu = \"qemu\"\n[machine]", "", "qemu"},
		{"registry http", "[machine]", "registry = \"http://registry.example\"\n[machine]", "", "HTTPS"},
		{"registry slash", "[machine]", "registry = \"/\"\n[machine]", "", "registry"},
		{"empty keys", "[machine]", "release_keys = []\n[machine]", "", "release_keys"},
		{"bad key", "[machine]", "release_keys = [\"abc\"]\n[machine]", "", "release_keys"},
	} {
		dir := tc.dir
		if dir == "" {
			dir = "/srv/relay"
		}
		_, err := Parse([]byte(strings.Replace(valid, tc.from, tc.to, 1)), dir)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
