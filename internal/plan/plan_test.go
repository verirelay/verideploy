package plan

import (
	"bytes"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/verirelay/protocol/guest"

	"github.com/verirelay/deploy/internal/config"
	"github.com/verirelay/deploy/internal/fake"
	"github.com/verirelay/deploy/internal/release"
)

var update = flag.Bool("update", false, "rewrite testdata/verirelay-relay.service")

const (
	dir = "/home/ubuntu/docker/verirelay/relay"
	tag = "rel-2026.10.30-1"
)

func production() (config.Config, release.Release) {
	c := config.Config{
		Dir: dir, Instance: "relay", Release: tag, Bootstrap: dir + "/bootstrap.json", VeriProv: "https://veriprov.verirelay.com",
		VCPUs: 8, Memory: "16G", MemoryMiB: 16384, Forward: netip.MustParseAddrPort("127.0.0.1:18081"),
		QEMU: config.DefaultQEMU,
	}
	r := release.Release{
		Tag: tag, Dir: dir + "/releases/" + tag, VCPUs: 8,
		Measurement:  "72d1b304b2c9b2fc237251cca3732bda33979492d5c179ee5aea5de601ddc05c06443e2ca87cf7c42686e6ce8c846906",
		VCPUType:     fake.VCPUType,
		KernelAppend: fake.KernelAppend,
		Record:       release.Statement{GitCommit: fake.Commit},
	}
	return c, r
}

// execStart splits the ExecStart line of a unit into words, as systemd does
// for the subset these units use: continuation lines and double quotes.
func execStart(t *testing.T, unit string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(strings.ReplaceAll(unit, "\\\n", " "), "\nExecStart=")
	if !ok {
		t.Fatal("no ExecStart")
	}
	line, _, _ := strings.Cut(rest, "\n")
	var words []string
	var word strings.Builder
	quoted, inWord := false, false
	for i := 0; i < len(line); i++ {
		switch ch := line[i]; {
		case ch == '"':
			quoted, inWord = !quoted, true
		case ch == '\\' && quoted:
			i++
			word.WriteByte(line[i])
		case ch == ' ' && !quoted:
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteByte(ch)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// TestProductionArgv reproduces the od3 production unit of 2026-10-30, apart
// from Deploy's absolute paths.
func TestProductionArgv(t *testing.T) {
	od3, err := os.ReadFile("testdata/od3-verirelay.service")
	if err != nil {
		t.Fatal(err)
	}
	want := execStart(t, string(od3))
	rel := dir + "/releases/" + tag + "/"
	for i, w := range want {
		switch w {
		case "OVMF.fd", "vmlinuz", "initrd.img":
			want[i] = rel + w
		case "file=config.img,if=virtio,format=raw,readonly=on":
			want[i] = "file=" + dir + "/config.img,if=virtio,format=raw,readonly=on"
		}
	}
	c, r := production()
	p, err := Resolve(c, r, []byte(fake.Bootstrap))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", p.Argv, want)
	}
	if got := execStart(t, p.Unit); !slices.Equal(got, p.Argv) {
		t.Fatalf("unit ExecStart\n got %q\nwant %q", got, p.Argv)
	}
	if p.HostData != fake.BootstrapHostData || p.VeriProv != "https://veriprov.verirelay.com" || p.Commit != fake.Commit {
		t.Fatalf("%+v", p)
	}
	if p.UnitPath != "/etc/systemd/system/verirelay-relay.service" || p.ConfigDisk != dir+"/config.img" {
		t.Fatalf("paths %s %s", p.UnitPath, p.ConfigDisk)
	}

	golden := filepath.Join("testdata", "verirelay-relay.service")
	if *update {
		if err := os.WriteFile(golden, []byte(p.Unit), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wantUnit, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if p.Unit != string(wantUnit) {
		t.Fatalf("unit differs from %s; rerun with -update and review\n%s", golden, p.Unit)
	}
}

func TestResolveRejects(t *testing.T) {
	c, r := production()
	if _, err := Resolve(c, r, []byte(`{"version":"veriprov-bootstrap/1"}`)); err == nil {
		t.Fatal("accepted an invalid Bootstrap")
	}
	if _, err := Resolve(c, r, []byte("not json")); err == nil {
		t.Fatal("accepted a non-JSON Bootstrap")
	}
	other := r
	other.VCPUs = 4
	if _, err := Resolve(c, other, []byte(fake.Bootstrap)); err == nil {
		t.Fatal("accepted a release entry for another vCPU count")
	}
	moved := c
	moved.VeriProv = "https://veriprov.example.com"
	if _, err := Resolve(moved, r, []byte(fake.Bootstrap)); err == nil || !strings.Contains(err.Error(), "run deploy fetch") {
		t.Fatalf("accepted the Bootstrap of another VeriProv: %v", err)
	}
}

// TestBootstrapBytes: the Bootstrap's exact bytes are authoritative, so a
// trailing newline is accepted and changes HOST_DATA.
func TestBootstrapBytes(t *testing.T) {
	c, r := production()
	p, err := Resolve(c, r, []byte(fake.Bootstrap+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.HostData == fake.BootstrapHostData {
		t.Fatal("trailing newline did not change HOST_DATA")
	}
	disk := ConfigDisk([]byte(fake.Bootstrap))
	got, err := guest.BootstrapFromConfigDisk(disk)
	if len(disk) != ConfigDiskSize || err != nil || !bytes.Equal(got, []byte(fake.Bootstrap)) {
		t.Fatalf("config disk: %d bytes, %v", len(disk), err)
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"-smp":                     "-smp",
		"host-data=ab+/c=":         "host-data=ab+/c=",
		"console=ttyS0 quiet":      `"console=ttyS0 quiet"`,
		`a"b\c`:                    `"a\"b\\c"`,
		"100%":                     `"100%%"`,
		"$HOME":                    `"$$HOME"`,
		";":                        `";"`,
		"user,id=net0,hostfwd=tcp": "user,id=net0,hostfwd=tcp",
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}
