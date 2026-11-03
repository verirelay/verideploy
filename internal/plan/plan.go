// Package plan derives everything an instance launches with from its config,
// its verified release and its Bootstrap.
package plan

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/verirelay/protocol/guest"

	"github.com/verirelay/verideploy/internal/config"
	"github.com/verirelay/verideploy/internal/release"
)

const (
	Version = "verirelay-deploy-plan/1"
	// GuestPort is the port VeriRelay listens on inside the CVM.
	GuestPort = 8080
	// ConfigDiskSize is the size of the Config Disk: the Bootstrap followed by
	// zero padding.
	ConfigDiskSize = 64 << 10
	// SystemUnitDir holds the units of the system manager.
	SystemUnitDir = "/etc/systemd/system"
)

// Scope is the systemd manager that runs an instance: the system manager
// when VeriDeploy runs as root, otherwise the user's own (systemctl --user), and
// QEMU then runs as that user.
type Scope struct {
	User bool
	// Dir is where the unit file goes.
	Dir string
}

// Plan is what an instance launches with. It holds no secrets.
type Plan struct {
	Version     string   `json:"version"`
	Instance    string   `json:"instance"`
	Release     string   `json:"release"`
	Commit      string   `json:"commit"`
	VCPUs       int      `json:"vcpus"`
	Memory      string   `json:"memory"`
	Measurement string   `json:"measurement"`
	HostData    string   `json:"host_data"`
	VeriProv    string   `json:"veriprov"`
	Forward     string   `json:"forward"`
	ConfigDisk  string   `json:"config_disk"`
	Argv        []string `json:"argv"`
	UserUnit    bool     `json:"user_unit"`
	UnitPath    string   `json:"unit_path"`
	Unit        string   `json:"unit"`
}

// Owns reports whether unit, an installed unit file, was generated for the
// same config file as p: its first line names that file.
func (p Plan) Owns(unit string) bool {
	first, _, _ := strings.Cut(unit, "\n")
	own, _, _ := strings.Cut(p.Unit, "\n")
	return first == own
}

// UnitName returns the systemd unit name of an instance.
func UnitName(instance string) string { return "verirelay-" + instance + ".service" }

// Path returns the path of an instance's plan.json.
func Path(dir string) string { return filepath.Join(dir, "plan.json") }

// ConfigDisk returns the Config Disk image of a Bootstrap.
func ConfigDisk(bootstrap []byte) []byte {
	disk := make([]byte, ConfigDiskSize)
	copy(disk, bootstrap)
	return disk
}

// Resolve derives the Plan for a unit in s. The QEMU options that enter the
// Launch Measurement come only from r; c chooses the vCPU count, memory,
// paths and port forward.
func Resolve(c config.Config, r release.Release, bootstrap []byte, s Scope) (Plan, error) {
	b, err := guest.DecodeBootstrap(bootstrap)
	if err == nil {
		err = b.Validate()
	}
	if err != nil {
		return Plan{}, fmt.Errorf("bootstrap %s: %w", c.Bootstrap, err)
	}
	if b.URL != c.VeriProv || b.Profile != c.Profile {
		return Plan{}, fmt.Errorf("%s is the Bootstrap of VeriProv %s with profile %q, but deploy.toml names %s and %q; run verideploy fetch", c.Bootstrap, b.URL, b.Profile, c.VeriProv, c.Profile)
	}
	if r.VCPUs != c.VCPUs || r.Tag != c.Release {
		return Plan{}, fmt.Errorf("release %s for %d vCPU does not match the config", r.Tag, r.VCPUs)
	}
	hostData := guest.HostData(bootstrap)
	disk := filepath.Join(c.Dir, "config.img")
	groups := [][]string{
		{c.QEMU},
		{"-enable-kvm", "-cpu", r.VCPUType, "-smp", strconv.Itoa(c.VCPUs), "-m", c.Memory},
		{"-machine", "q35,confidential-guest-support=sev0,memory-encryption=sev0,memory-backend=ram0,vmport=off"},
		{"-object", "memory-backend-memfd,id=ram0,size=" + c.Memory + ",share=on"},
		{"-object", "sev-snp-guest,id=sev0,cbitpos=51,reduced-phys-bits=1,kernel-hashes=on,policy=0x30000,host-data=" + base64.StdEncoding.EncodeToString(hostData[:])},
		{"-bios", r.Path("OVMF.fd"), "-kernel", r.Path("vmlinuz"), "-initrd", r.Path("initrd.img")},
		{"-append", r.KernelAppend},
		{"-drive", "file=" + disk + ",if=virtio,format=raw,readonly=on"},
		{"-display", "none", "-monitor", "none", "-serial", "stdio"},
		{"-netdev", "user,id=net0,hostfwd=tcp:" + c.Forward.String() + "-:" + strconv.Itoa(GuestPort), "-device", "virtio-net-pci,netdev=net0"},
	}
	p := Plan{
		Version:     Version,
		Instance:    c.Instance,
		Release:     r.Tag,
		Commit:      r.Record.GitCommit,
		VCPUs:       c.VCPUs,
		Memory:      c.Memory,
		Measurement: r.Measurement,
		HostData:    hex.EncodeToString(hostData[:]),
		VeriProv:    b.URL,
		Forward:     c.Forward.String(),
		ConfigDisk:  disk,
		UserUnit:    s.User,
		UnitPath:    filepath.Join(s.Dir, UnitName(c.Instance)),
		Unit:        unit(c, r, groups, s.User),
	}
	for _, g := range groups {
		p.Argv = append(p.Argv, g...)
	}
	return p, nil
}

// unit renders the unit file. A user manager has neither network-online.target
// nor multi-user.target, so a user unit starts with default.target.
func unit(c config.Config, r release.Release, groups [][]string, user bool) string {
	lines := make([]string, len(groups))
	for i, g := range groups {
		words := make([]string, len(g))
		for j, w := range g {
			words[j] = quote(w)
		}
		lines[i] = strings.Join(words, " ")
	}
	network, target := "After=network-online.target\nWants=network-online.target\n", "multi-user.target"
	if user {
		network, target = "", "default.target"
	}
	return fmt.Sprintf(`# Generated by verideploy from %s/deploy.toml. Change that file and run verideploy up.
[Unit]
Description=VeriRelay CVM (%s, %d vCPU)
%s
[Service]
WorkingDirectory=%s
ExecStart=%s
Restart=always
RestartSec=5

[Install]
WantedBy=%s
`, c.Dir, r.Tag, c.VCPUs, network, c.Dir, strings.Join(lines, " \\\n  "), target)
}

var bareWord = regexp.MustCompile(`^[A-Za-z0-9_@+=:,./-]+$`)

// quote renders one ExecStart word: bare when systemd reads it literally,
// otherwise double-quoted with backslash escapes. % and $ are doubled so
// systemd expands neither specifiers nor variables.
func quote(word string) string {
	if bareWord.MatchString(word) {
		return word
	}
	word = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(word)
	return `"` + word + `"`
}
