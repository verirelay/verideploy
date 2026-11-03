package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verirelay/verideploy/internal/fake"
	"github.com/verirelay/verideploy/internal/network"
)

const (
	tag         = "rel-2026.10.30-1"
	officialKey = "c0quXUfgy8ZYKZA-oXVzEDmU75WuaT2xKx5W-3EHqaI"
)

func official(t *testing.T) []ed25519.PublicKey {
	key, err := base64.RawURLEncoding.DecodeString(officialKey)
	if err != nil {
		t.Fatal(err)
	}
	return []ed25519.PublicKey{key}
}

func public(k ed25519.PrivateKey) []ed25519.PublicKey {
	return []ed25519.PublicKey{k.Public().(ed25519.PublicKey)}
}

// TestPublishedRelease checks the real rel-2026.10.30-1 metadata: every Launch
// Measurement has a Build Record signed by the official Release Key.
func TestPublishedRelease(t *testing.T) {
	dir := filepath.Join("testdata", tag)
	var c chain
	if err := readJSON(filepath.Join(dir, "chain.json"), &c); err != nil {
		t.Fatal(err)
	}
	records, err := os.ReadFile(filepath.Join(dir, "build-records.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2, 4, 8, 16} {
		m, vcpuType, appendLine, err := launchEntry(c, n)
		if err != nil {
			t.Fatal(err)
		}
		if vcpuType != "EPYC-v4" || appendLine != "console=ttyS0 quiet panic=1" {
			t.Fatalf("%d vCPU: %s %q", n, vcpuType, appendLine)
		}
		s, keyID, err := VerifyBuildRecords(records, m, tag, fake.Repository, official(t))
		if err != nil {
			t.Fatalf("%d vCPU: %v", n, err)
		}
		if s.GitCommit != fake.Commit || keyID != "sha256:7d646b0eef9f8fd623b1a12e6b83b1ee061622d96999c9ad3bc813cf8aaf929c" {
			t.Fatalf("%d vCPU: %+v %s", n, s, keyID)
		}
	}
	if m, _, _, _ := launchEntry(c, 8); !strings.HasPrefix(m, "72d1b304") {
		t.Fatalf("8 vCPU measurement %s", m)
	}
	if _, _, _, err := launchEntry(c, 3); err == nil || !strings.Contains(err.Error(), "1, 2, 4, 8, 16") {
		t.Fatalf("3 vCPU: %v", err)
	}
	sums, err := readSums(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range BootFiles {
		if c.Artifacts[name].SHA256 != sums[name] {
			t.Fatalf("%s: chain and SHA256SUMS disagree", name)
		}
	}
}

func TestOpen(t *testing.T) {
	key := fake.Key(1)
	dir := fake.WriteRelease(t, t.TempDir(), tag, key, 1, 8)
	r, err := Open(dir, tag, 8, fake.Repository, public(key))
	if err != nil {
		t.Fatal(err)
	}
	if r.Measurement != fake.Measurement(8) || r.VCPUType != fake.VCPUType || r.KernelAppend != fake.KernelAppend || r.Record.GitCommit != fake.Commit {
		t.Fatalf("%+v", r)
	}
	if r.Path("OVMF.fd") != filepath.Join(dir, "OVMF.fd") {
		t.Fatal(r.Path("OVMF.fd"))
	}
}

func TestOpenRejects(t *testing.T) {
	key := fake.Key(1)
	editChain := func(edit func(map[string]any)) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			path := filepath.Join(dir, "chain.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var c map[string]any
			if err := json.Unmarshal(data, &c); err != nil {
				t.Fatal(err)
			}
			edit(c)
			fake.WriteJSON(t, path, c)
		}
	}
	params := func(c map[string]any) map[string]any {
		return c["launch_measurements"].([]any)[0].(map[string]any)["params"].(map[string]any)
	}
	records := func(rs ...fake.Record) func(*testing.T, string) {
		return func(t *testing.T, dir string) { fake.WriteJSON(t, filepath.Join(dir, "build-records.json"), rs) }
	}
	other := fake.Sign(fake.Key(2), tag, fake.Measurement(8))
	wrongTag := fake.Sign(key, "rel-2026.10.29-1", fake.Measurement(8))
	forged := fake.Sign(key, tag, fake.Measurement(8))
	forged.Statement.GitCommit = strings.Repeat("0", 40)
	for _, tc := range []struct {
		name  string
		vcpus int
		edit  func(*testing.T, string)
		want  string
	}{
		{"artifact byte", 8, func(t *testing.T, dir string) { fake.WriteFile(t, filepath.Join(dir, "vmlinuz"), []byte("other")) }, "vmlinuz has sha256"},
		{"sums vs chain", 8, func(t *testing.T, dir string) {
			fake.WriteFile(t, filepath.Join(dir, "SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  OVMF.fd\n"))
		}, "disagree on OVMF.fd"},
		{"sums syntax", 8, func(t *testing.T, dir string) {
			fake.WriteFile(t, filepath.Join(dir, "SHA256SUMS"), []byte("nonsense\n"))
		}, "malformed"},
		{"tag", 8, editChain(func(c map[string]any) { c["release"] = map[string]any{"tag": "rel-x"} }), "chain.json is for release"},
		{"profile", 8, editChain(func(c map[string]any) { c["launch"].(map[string]any)["measurement_profile"] = "other/1" }), "not supported"},
		{"guest features", 8, editChain(func(c map[string]any) { params(c)["guest_features"] = "0x3" }), "guest_features"},
		{"params vs launch", 8, editChain(func(c map[string]any) { params(c)["kernel_append"] = "quiet" }), "disagrees"},
		{"vcpus", 4, nil, "no Launch Measurement for 4 vCPU"},
		{"no records", 8, func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "build-records.json")); err != nil {
				t.Fatal(err)
			}
		}, "published without Registry"},
		{"untrusted key", 8, records(other), "trusted Release Key"},
		{"wrong tag", 8, records(wrongTag), "does not name release"},
		{"forged field", 8, records(forged), "trusted Release Key"},
		{"no record", 8, records(fake.Sign(key, tag, fake.Measurement(16))), "no Build Record for measurement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fake.WriteRelease(t, t.TempDir(), tag, key, 8)
			if tc.edit != nil {
				tc.edit(t, dir)
			}
			_, err := Open(dir, tag, tc.vcpus, fake.Repository, public(key))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFetch(t *testing.T) {
	key := fake.Key(1)
	src := fake.WriteRelease(t, t.TempDir(), tag, key, 8)
	requests := map[string]int{}
	// Like GitHub, the download URL redirects to a signed URL on the file host.
	files := httptest.NewServer(http.StripPrefix("/files/", http.FileServer(http.Dir(src))))
	defer files.Close()
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/releases/download/"+tag+"/")
		requests[name]++
		http.Redirect(w, r, files.URL+"/files/"+name+"?sig=abc&se=2026-11-03", http.StatusFound)
	}))
	defer repo.Close()
	dir := filepath.Join(t.TempDir(), "releases", tag)
	verify := func(d string) error {
		_, err := Open(d, tag, 8, fake.Repository, public(key))
		return err
	}
	fetch := func(repository, dir string) error {
		var log strings.Builder
		return Fetch(t.Context(), network.Client(true), repository, tag, dir, verify, &log)
	}
	if err := fetch(repo.URL, dir); err != nil {
		t.Fatal(err)
	}
	if err := verify(dir); err != nil {
		t.Fatal(err)
	}
	// A verified release is kept as it is.
	if err := fetch(repo.URL, dir); err != nil || requests["vmlinuz"] != 1 || requests["chain.json"] != 1 {
		t.Fatalf("refetch: %v, requests %v", err, requests)
	}

	// A broken release is replaced only by one that verifies.
	fake.WriteFile(t, filepath.Join(src, "initrd.img"), []byte("tampered"))
	if err := os.Remove(filepath.Join(dir, "initrd.img")); err != nil {
		t.Fatal(err)
	}
	if err := fetch(repo.URL, dir); err == nil || !strings.Contains(err.Error(), "SHA256SUMS says") {
		t.Fatalf("tampered download: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging left behind: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vmlinuz")); err != nil {
		t.Fatalf("existing files touched: %v", err)
	}
	fake.WriteRelease(t, src, tag, key, 8)
	if err := fetch(repo.URL, dir); err != nil {
		t.Fatal(err)
	}
	if err := verify(dir); err != nil {
		t.Fatal(err)
	}

	// A release without build-records.json does not verify and is not kept.
	src2 := fake.WriteRelease(t, t.TempDir(), tag, key, 8)
	if err := os.Remove(filepath.Join(src2, "build-records.json")); err != nil {
		t.Fatal(err)
	}
	files2 := httptest.NewServer(http.StripPrefix("/releases/download/"+tag+"/", http.FileServer(http.Dir(src2))))
	defer files2.Close()
	fresh := filepath.Join(t.TempDir(), tag)
	if err := fetch(files2.URL, fresh); err == nil || !strings.Contains(err.Error(), "without Registry") {
		t.Fatalf("fetch without records: %v", err)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("unverified release kept: %v", err)
	}
}
