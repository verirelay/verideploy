// Package config loads and validates the deploy.toml file of an instance.
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/verirelay/protocol/avr1"

	"github.com/verirelay/verideploy/internal/network"
)

const (
	Version                  = "verirelay-deploy-config/1"
	DefaultQEMU              = "/usr/bin/qemu-system-x86_64"
	DefaultReleaseRepository = "https://github.com/verirelay/release"
	DefaultRegistry          = "https://registry.verirelay.com"
	// OfficialReleaseKey is the Release Key of verirelay/release, pinned
	// independently of Registry, as in the Verifier.
	OfficialReleaseKey = "c0quXUfgy8ZYKZA-oXVzEDmU75WuaT2xKx5W-3EHqaI"
	MinMemoryMiB       = 512
	maxFileSize        = 64 << 10
)

var (
	instancePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	tagPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	memoryPattern   = regexp.MustCompile(`^([1-9][0-9]{0,6})([MG])$`)
	profilePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)
	// pathPattern keeps paths literal in QEMU options, where a comma is a
	// separator, and in the systemd unit.
	pathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/+-]*$`)
)

// Config is a validated instance. Paths are absolute.
type Config struct {
	// Path is the config file; Dir is its directory, the instance directory.
	Path     string
	Dir      string
	Instance string
	Release  string
	// VeriProv is the HTTPS origin the Bootstrap of Profile is fetched from.
	VeriProv string
	Profile  string
	// Bootstrap is where fetch stores that Bootstrap: bootstrap.json in Dir.
	Bootstrap         string
	VCPUs             int
	Memory            string
	MemoryMiB         int64
	Forward           netip.AddrPort
	QEMU              string
	ReleaseRepository string
	ReleaseKeys       []ed25519.PublicKey
	// Registry is the revocation lookup base URL; empty turns it off.
	Registry string
}

type file struct {
	Version           string   `toml:"version"`
	Instance          string   `toml:"instance"`
	Release           string   `toml:"release"`
	VeriProv          string   `toml:"veriprov"`
	Profile           string   `toml:"profile"`
	QEMU              *string  `toml:"qemu"`
	ReleaseRepository *string  `toml:"release_repository"`
	ReleaseKeys       []string `toml:"release_keys"`
	Registry          *string  `toml:"registry"`
	Machine           struct {
		VCPUs  int    `toml:"vcpus"`
		Memory string `toml:"memory"`
	} `toml:"machine"`
	Network struct {
		Forward string `toml:"forward"`
	} `toml:"network"`
}

// Load reads path strictly: unknown keys fail. The instance directory is the
// directory of path.
func Load(path string) (Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Config{}, err
	}
	if len(data) > maxFileSize {
		return Config{}, fmt.Errorf("%s exceeds %d bytes", path, maxFileSize)
	}
	c, err := Parse(data, filepath.Dir(abs))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = abs
	return c, nil
}

// Parse decodes a config file whose instance directory is dir.
func Parse(data []byte, dir string) (Config, error) {
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return Config{}, err
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return Config{}, fmt.Errorf("unknown key %q", keys[0].String())
	}
	if f.Version != Version {
		return Config{}, fmt.Errorf("version = %q, want %q", f.Version, Version)
	}
	c := Config{
		Dir:               dir,
		Instance:          f.Instance,
		Release:           f.Release,
		VeriProv:          f.VeriProv,
		Profile:           f.Profile,
		Bootstrap:         filepath.Join(dir, "bootstrap.json"),
		VCPUs:             f.Machine.VCPUs,
		Memory:            f.Machine.Memory,
		QEMU:              orDefault(f.QEMU, DefaultQEMU),
		ReleaseRepository: orDefault(f.ReleaseRepository, DefaultReleaseRepository),
		Registry:          orDefault(f.Registry, DefaultRegistry),
	}
	if !pathPattern.MatchString(dir) {
		return Config{}, fmt.Errorf("instance directory %q may contain only A-Z a-z 0-9 . _ / + -", dir)
	}
	if err := CheckInstance(c.Instance); err != nil {
		return Config{}, err
	}
	if err := CheckRelease(c.Release); err != nil {
		return Config{}, err
	}
	if err := CheckVeriProv(c.VeriProv); err != nil {
		return Config{}, err
	}
	if err := CheckProfile(c.Profile); err != nil {
		return Config{}, err
	}
	if c.VCPUs < 1 {
		return Config{}, errors.New("machine.vcpus is required")
	}
	if c.MemoryMiB, err = ParseMemory(c.Memory); err != nil {
		return Config{}, err
	}
	if c.Forward, err = ParseForward(f.Network.Forward); err != nil {
		return Config{}, err
	}
	if !pathPattern.MatchString(c.QEMU) {
		return Config{}, fmt.Errorf("qemu %q must be an absolute path of A-Z a-z 0-9 . _ / + -", c.QEMU)
	}
	if err := network.CheckURL(c.ReleaseRepository); err != nil {
		return Config{}, fmt.Errorf("release_repository: %w", err)
	}
	c.ReleaseRepository = strings.TrimSuffix(c.ReleaseRepository, "/")
	if c.Registry != "" {
		if err := network.CheckURL(c.Registry); err != nil {
			return Config{}, fmt.Errorf("registry: %w", err)
		}
		c.Registry = strings.TrimSuffix(c.Registry, "/")
	}
	keys := f.ReleaseKeys
	if keys == nil {
		keys = []string{OfficialReleaseKey}
	}
	if len(keys) == 0 {
		return Config{}, errors.New("release_keys must not be empty")
	}
	for _, k := range keys {
		key, err := avr1.DecodeEd25519PublicKeyBase64URL(k)
		if err != nil {
			return Config{}, fmt.Errorf("release_keys: %q is not an unpadded base64url Ed25519 public key", k)
		}
		c.ReleaseKeys = append(c.ReleaseKeys, key)
	}
	return c, nil
}

func orDefault(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

// CheckInstance validates an instance name.
func CheckInstance(s string) error {
	if !instancePattern.MatchString(s) {
		return errors.New("instance must be 1 to 32 characters of a-z 0-9 -, starting with a letter or digit")
	}
	return nil
}

// CheckRelease validates a release tag.
func CheckRelease(s string) error {
	if !tagPattern.MatchString(s) {
		return fmt.Errorf("release %q is not a release tag", s)
	}
	return nil
}

// CheckVeriProv requires an HTTPS origin, the form a Bootstrap's url takes:
// an explicit port is allowed; credentials, path, query and fragment are not.
func CheckVeriProv(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || strings.ContainsAny(s, "?#") {
		return fmt.Errorf("veriprov %q must be an HTTPS origin, such as https://veriprov.example.com", s)
	}
	return nil
}

// CheckProfile validates a Bootstrap profile; empty selects the Bootstrap
// without one.
func CheckProfile(s string) error {
	if !profilePattern.MatchString(s) {
		return fmt.Errorf("profile %q must be at most 64 characters of A-Z a-z 0-9 . _ -", s)
	}
	return nil
}

// ParseMemory returns the MiB of a memory size such as 16G.
func ParseMemory(s string) (int64, error) {
	m := memoryPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("machine.memory %q must be a whole number of M or G, such as 16G", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	if m[2] == "G" {
		n *= 1024
	}
	if n < MinMemoryMiB {
		return 0, fmt.Errorf("machine.memory %q is below %dM", s, MinMemoryMiB)
	}
	return n, nil
}

// ParseForward parses a host IPv4 address and port.
func ParseForward(s string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil || !ap.Addr().Is4() || ap.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("network.forward %q must be an IPv4 address and port, such as 127.0.0.1:18081", s)
	}
	return ap, nil
}
