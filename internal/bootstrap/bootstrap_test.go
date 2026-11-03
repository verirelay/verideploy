package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verirelay/verideploy/internal/fake"
)

func TestFetch(t *testing.T) {
	vp := &fake.VeriProv{}
	server := httptest.NewTLSServer(vp)
	defer server.Close()
	vp.Bootstraps = [][]byte{fake.BootstrapFor(server.URL, ""), fake.BootstrapFor(server.URL, "b")}
	for _, profile := range []string{"", "b"} {
		got, err := Fetch(t.Context(), server.Client(), server.URL, profile)
		if err != nil || string(got) != string(fake.BootstrapFor(server.URL, profile)) {
			t.Fatalf("profile %q: %q, %v", profile, got, err)
		}
	}
	if _, err := Fetch(t.Context(), server.Client(), server.URL, "c"); err == nil || !strings.Contains(err.Error(), `it has "", "b"`) {
		t.Fatalf("unknown profile: %v", err)
	}
}

func TestSelectRejects(t *testing.T) {
	const origin = "https://veriprov.example.com"
	entry := func(data []byte, hostData string) []Entry {
		vp := &fake.VeriProv{Bootstraps: [][]byte{data}}
		server := httptest.NewTLSServer(vp)
		defer server.Close()
		entries, err := List(t.Context(), server.Client(), server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if hostData != "" {
			entries[0].HostData = hostData
		}
		return entries
	}
	for _, tc := range []struct {
		name    string
		entries []Entry
		want    string
	}{
		{"other origin", entry(fake.BootstrapFor("https://other.example.com", ""), ""), "names VeriProv https://other.example.com"},
		{"wrong HOST_DATA", entry(fake.BootstrapFor(origin, ""), strings.Repeat("0", 64)), "does not hash"},
		{"invalid", entry([]byte(`{"version":"veriprov-bootstrap/1"}`), ""), "invalid"},
	} {
		if _, err := Select(tc.entries, origin, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestOldVeriProv(t *testing.T) {
	// VeriProv v0.1.0 answers an unknown route with 400.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"invalid_request.invalid_field"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	if _, err := List(t.Context(), server.Client(), server.URL); err == nil || !strings.Contains(err.Error(), "v0.2.0 and later") {
		t.Fatalf("old VeriProv: %v", err)
	}
}
