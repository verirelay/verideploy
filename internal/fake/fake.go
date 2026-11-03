// Package fake builds synthetic releases and a fake VeriRelay CVM for tests.
package fake

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/verirelay/protocol/avr1"
)

const (
	Repository   = "https://github.com/verirelay/release"
	VCPUType     = "EPYC-v4"
	KernelAppend = "console=ttyS0 quiet panic=1"
	CommittedAt  = "2026-10-30T05:30:01Z"
	Commit       = "3648c29c94590db1bc37e90be1b74fe2e5b24da2"
	// Bootstrap is the production Bootstrap of od3, byte for byte; its
	// SHA-256 is BootstrapHostData.
	Bootstrap         = `{"version":"veriprov-bootstrap/1","protocol":"veriprov/1","url":"https://veriprov.verirelay.com","identity_public_key":"E0mn567myKlD1eCT3Ux89j0IoSwK_zJ4BdQtBrq7Bbs"}`
	BootstrapHostData = "0433d7cb88ed3e58e5ca0663b3033da88fcfdc23cf477affc858671a16dafad6"
)

// Key returns a deterministic Release Key for tests.
func Key(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes32(seed))
}

func bytes32(b byte) []byte {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = b
	}
	return s
}

// Measurement returns the synthetic Launch Measurement of a vCPU count.
func Measurement(vcpus int) string { return fmt.Sprintf("%096x", vcpus) }

// Record is one signed Build Record of build-records.json.
type Record struct {
	Statement struct {
		Version     string `json:"version"`
		Repository  string `json:"repository"`
		GitCommit   string `json:"git_commit"`
		GitTag      string `json:"git_tag"`
		Measurement string `json:"measurement"`
		CommittedAt string `json:"committed_at"`
	} `json:"statement"`
	Signature string `json:"signature"`
}

// Sign returns a Build Record for measurement and tag signed by key.
func Sign(key ed25519.PrivateKey, tag, measurement string) Record {
	var r Record
	r.Statement.Version = "verirelay-build-record/1"
	r.Statement.Repository = Repository
	r.Statement.GitCommit = Commit
	r.Statement.GitTag = tag
	r.Statement.Measurement = measurement
	r.Statement.CommittedAt = CommittedAt
	return r.Resign(key)
}

// Resign signs the current statement with key.
func (r Record) Resign(key ed25519.PrivateKey) Record {
	payload, err := json.Marshal(r.Statement)
	if err != nil {
		panic(err)
	}
	r.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, payload))
	return r
}

// WriteRelease writes a release to dir with launch entries for vcpus and a
// build-records.json signed by key, and returns dir.
func WriteRelease(t testing.TB, dir, tag string, key ed25519.PrivateKey, vcpus ...int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	type entry struct {
		VCPUs       int               `json:"vcpus"`
		Measurement string            `json:"measurement"`
		Params      map[string]string `json:"params"`
	}
	chain := map[string]any{
		"release": map[string]string{"tag": tag},
		"launch": map[string]string{
			"measurement_profile": "sev-snp-qemu-direct-boot/1", "vcpu_type": VCPUType, "kernel_append": KernelAppend,
		},
	}
	artifacts := map[string]map[string]string{}
	var sums strings.Builder
	for _, name := range []string{"OVMF.fd", "vmlinuz", "initrd.img"} {
		data := []byte(name + " of " + tag)
		WriteFile(t, filepath.Join(dir, name), data)
		sum := sha256.Sum256(data)
		artifacts[name] = map[string]string{"sha256": hex.EncodeToString(sum[:])}
		fmt.Fprintf(&sums, "%x  %s\n", sum, name)
	}
	chain["artifacts"] = artifacts
	var entries []entry
	var records []Record
	for _, n := range vcpus {
		entries = append(entries, entry{n, Measurement(n), map[string]string{
			"mode": "snp", "vmm_type": "QEMU", "vcpu_type": VCPUType, "guest_features": "0x1", "kernel_append": KernelAppend,
		}})
		records = append(records, Sign(key, tag, Measurement(n)))
	}
	chain["launch_measurements"] = entries
	WriteFile(t, filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()))
	WriteJSON(t, filepath.Join(dir, "chain.json"), chain)
	WriteJSON(t, filepath.Join(dir, "build-records.json"), records)
	return dir
}

// WriteFile writes data to path or fails the test.
func WriteFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// WriteJSON writes v as JSON to path or fails the test.
func WriteJSON(t testing.TB, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	WriteFile(t, path, data)
}

// BootstrapFor returns a Bootstrap naming origin and profile, in the byte
// form VeriProv's deployment docs produce.
func BootstrapFor(origin, profile string) []byte {
	if profile != "" {
		profile = `,"profile":"` + profile + `"`
	}
	return fmt.Appendf(nil, `{"version":"veriprov-bootstrap/1","protocol":"veriprov/1","url":%q,"identity_public_key":"E0mn567myKlD1eCT3Ux89j0IoSwK_zJ4BdQtBrq7Bbs"%s}`, origin, profile)
}

// VeriProv answers /readyz and GET /v1/bootstraps like a VeriProv with one
// deployment per Bootstrap. Set Bootstraps before serving.
type VeriProv struct {
	Bootstraps [][]byte
}

func (v *VeriProv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/readyz":
		_, _ = w.Write([]byte("ok\n"))
	case "/v1/bootstraps":
		type entry struct {
			Profile   string `json:"profile"`
			HostData  string `json:"host_data"`
			Bootstrap string `json:"bootstrap"`
		}
		var entries []entry
		for _, b := range v.Bootstraps {
			var decoded struct {
				Profile string `json:"profile"`
			}
			_ = json.Unmarshal(b, &decoded)
			sum := sha256.Sum256(b)
			entries = append(entries, entry{decoded.Profile, hex.EncodeToString(sum[:]), base64.RawURLEncoding.EncodeToString(b)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"bootstraps": entries})
	default:
		http.Error(w, `{"error":{"code":"invalid_request.invalid_field"}}`, http.StatusBadRequest)
	}
}

// CVM answers /readyz and /v1/avr/attestation like a VeriRelay CVM launched
// with Measurement and the Bootstrap whose SHA-256 is HostData.
type CVM struct {
	Ready       bool
	Measurement string
	Bootstrap   []byte
	// WrongBinding makes REPORT_DATA bind another nonce.
	WrongBinding bool
	mu           sync.Mutex
}

// SetReady changes Ready while the CVM serves.
func (c *CVM) SetReady(ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Ready = ready
}

func (c *CVM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/readyz":
		if !c.Ready {
			http.Error(w, "not_ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	case "/v1/avr/attestation":
		c.attest(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (c *CVM) attest(w http.ResponseWriter, r *http.Request) {
	encoded := r.URL.Query().Get("nonce")
	nonce, err := avr1.DecodeNonceBase64URL(encoded)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := Key(9)
	pub := key.Public().(ed25519.PublicKey)
	if c.WrongBinding {
		nonce = make([]byte, len(nonce))
	}
	binding, err := avr1.ReportDataAVR1(pub, nonce)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	measurement, _ := hex.DecodeString(c.Measurement)
	hostData := sha256.Sum256(c.Bootstrap)
	report := make([]byte, avr1.AttestationReportSize)
	copy(report[0x50:], binding[:])
	copy(report[0x90:0xC0], measurement)
	copy(report[0xC0:], hostData[:])
	var resp avr1.AttestationResponse
	resp.Nonce = encoded
	resp.Evidence.Report = base64.RawURLEncoding.EncodeToString(report)
	resp.ReceiptSigningPublicKey = base64.RawURLEncoding.EncodeToString(pub)
	resp.Bootstrap = base64.RawURLEncoding.EncodeToString(c.Bootstrap)
	_ = json.NewEncoder(w).Encode(resp)
}
