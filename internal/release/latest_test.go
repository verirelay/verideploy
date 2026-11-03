package release

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verirelay/verideploy/internal/network"
)

func TestLatest(t *testing.T) {
	for _, tc := range []struct{ name, location, want string }{
		{"tag", "/verirelay/release/releases/tag/rel-2026.10.30-1", "rel-2026.10.30-1"},
		{"no release", "/verirelay/release/releases", ""},
		{"no redirect", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.location == "" {
					http.NotFound(w, r)
					return
				}
				http.Redirect(w, r, tc.location, http.StatusFound)
			}))
			defer server.Close()
			got, err := Latest(t.Context(), network.Client(false), server.URL)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("Latest = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
