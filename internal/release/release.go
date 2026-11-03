// Package release downloads and verifies a release of verirelay/release.
package release

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Profile is the only launch profile the fixed QEMU command line reproduces.
const Profile = "sev-snp-qemu-direct-boot/1"

const maxMetadataSize = 1 << 20

// BootFiles enter the Launch Measurement. MetadataFiles describe and sign them.
var (
	BootFiles     = []string{"OVMF.fd", "vmlinuz", "initrd.img"}
	MetadataFiles = []string{"SHA256SUMS", "chain.json", "build-records.json"}
)

var (
	measurementPattern = regexp.MustCompile(`^[0-9a-f]{96}$`)
	vcpuTypePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	sumLine            = regexp.MustCompile(`^([0-9a-f]{64})  ([A-Za-z0-9._-]+)$`)
)

// Release is a verified release directory and its launch entry for one vCPU
// count.
type Release struct {
	Tag          string
	Dir          string
	VCPUs        int
	Measurement  string
	VCPUType     string
	KernelAppend string
	// Record is the Build Record that signs Measurement for Tag.
	Record Statement
	// KeyID names the Release Key that signed Record.
	KeyID string
}

// Path returns the absolute path of a release file.
func (r Release) Path(name string) string { return filepath.Join(r.Dir, name) }

type chain struct {
	Release struct {
		Tag string `json:"tag"`
	} `json:"release"`
	Launch struct {
		MeasurementProfile string `json:"measurement_profile"`
		VCPUType           string `json:"vcpu_type"`
		KernelAppend       string `json:"kernel_append"`
	} `json:"launch"`
	Artifacts map[string]struct {
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
	LaunchMeasurements []struct {
		VCPUs       int    `json:"vcpus"`
		Measurement string `json:"measurement"`
		Params      struct {
			Mode          string `json:"mode"`
			VMMType       string `json:"vmm_type"`
			VCPUType      string `json:"vcpu_type"`
			GuestFeatures string `json:"guest_features"`
			KernelAppend  string `json:"kernel_append"`
		} `json:"params"`
	} `json:"launch_measurements"`
}

// Open verifies the release in dir against tag and returns its launch entry
// for vcpus: the boot files match SHA256SUMS and chain.json, the entry is one
// the fixed QEMU command line reproduces, and a Build Record in
// build-records.json, signed by one of keys, binds its measurement to tag and
// repository.
func Open(dir, tag string, vcpus int, repository string, keys []ed25519.PublicKey) (Release, error) {
	sums, err := readSums(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return Release{}, err
	}
	var c chain
	if err := readJSON(filepath.Join(dir, "chain.json"), &c); err != nil {
		return Release{}, err
	}
	if c.Release.Tag != tag {
		return Release{}, fmt.Errorf("chain.json is for release %q, want %q", c.Release.Tag, tag)
	}
	if c.Launch.MeasurementProfile != Profile {
		return Release{}, fmt.Errorf("launch profile %q is not supported; want %q", c.Launch.MeasurementProfile, Profile)
	}
	if err := checkBootFiles(dir, sums, c); err != nil {
		return Release{}, err
	}
	r := Release{Tag: tag, Dir: dir, VCPUs: vcpus}
	if r.Measurement, r.VCPUType, r.KernelAppend, err = launchEntry(c, vcpus); err != nil {
		return Release{}, fmt.Errorf("release %s: %w", tag, err)
	}
	records, err := os.ReadFile(filepath.Join(dir, "build-records.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Release{}, fmt.Errorf("release %s has no build-records.json: it was published without Registry, so no Release Key signs its measurements", tag)
	}
	if err != nil {
		return Release{}, err
	}
	r.Record, r.KeyID, err = VerifyBuildRecords(records, r.Measurement, tag, repository, keys)
	if err != nil {
		return Release{}, err
	}
	return r, nil
}

func checkBootFiles(dir string, sums map[string]string, c chain) error {
	for _, name := range BootFiles {
		want, ok := sums[name]
		if !ok {
			return fmt.Errorf("SHA256SUMS does not list %s", name)
		}
		if c.Artifacts[name].SHA256 != want {
			return fmt.Errorf("chain.json and SHA256SUMS disagree on %s", name)
		}
		got, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s has sha256 %s, SHA256SUMS says %s", name, got, want)
		}
	}
	return nil
}

// launchEntry returns the measurement, vcpu_type and kernel_append of the
// entry for vcpus, after checking that the fixed QEMU command line reproduces
// its parameters.
func launchEntry(c chain, vcpus int) (measurement, vcpuType, kernelAppend string, err error) {
	var counts []string
	for _, e := range c.LaunchMeasurements {
		counts = append(counts, strconv.Itoa(e.VCPUs))
		if e.VCPUs != vcpus {
			continue
		}
		p := e.Params
		switch {
		case p.Mode != "snp" || p.VMMType != "QEMU" || p.GuestFeatures != "0x1":
			return "", "", "", fmt.Errorf("launch entry for %d vCPU has mode %q, vmm_type %q, guest_features %q; want snp, QEMU, 0x1", vcpus, p.Mode, p.VMMType, p.GuestFeatures)
		case p.VCPUType != c.Launch.VCPUType || p.KernelAppend != c.Launch.KernelAppend:
			return "", "", "", fmt.Errorf("launch entry for %d vCPU disagrees with the launch profile", vcpus)
		case !measurementPattern.MatchString(e.Measurement):
			return "", "", "", fmt.Errorf("launch entry for %d vCPU has an invalid measurement", vcpus)
		case !vcpuTypePattern.MatchString(p.VCPUType):
			return "", "", "", fmt.Errorf("vcpu_type %q is not a QEMU CPU model name", p.VCPUType)
		case !printable(p.KernelAppend):
			return "", "", "", errors.New("kernel_append must be non-empty printable ASCII")
		}
		return e.Measurement, p.VCPUType, p.KernelAppend, nil
	}
	return "", "", "", fmt.Errorf("no Launch Measurement for %d vCPU; the release has %s", vcpus, strings.Join(counts, ", "))
}

func readSums(path string) (map[string]string, error) {
	data, err := readSmall(path)
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		m := sumLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			return nil, fmt.Errorf("SHA256SUMS: malformed line %q", scanner.Text())
		}
		if _, dup := sums[m[2]]; dup {
			return nil, fmt.Errorf("SHA256SUMS lists %s twice", m[2])
		}
		sums[m[2]] = m[1]
	}
	return sums, scanner.Err()
}

func readJSON(path string, v any) error {
	data, err := readSmall(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxMetadataSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMetadataSize {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxMetadataSize)
	}
	return data, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func printable(s string) bool {
	return s != "" && !slices.ContainsFunc([]byte(s), func(b byte) bool { return b < 0x20 || b > 0x7e })
}
