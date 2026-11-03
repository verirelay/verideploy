package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verirelay/verideploy/internal/fake"
	"github.com/verirelay/verideploy/internal/network"
)

func sign(t *testing.T, key ed25519.PrivateKey, statement any) proof {
	payload, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	return proof{
		Algorithm:     "ed25519",
		KeyID:         KeyID(key.Public().(ed25519.PublicKey)),
		SignedPayload: base64.RawURLEncoding.EncodeToString(payload),
		Signature:     base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, payload)),
	}
}

func TestCheckRegistry(t *testing.T) {
	key, other := fake.Key(1), fake.Key(2)
	record := Statement{
		Version: BuildRecordVersion, Repository: fake.Repository, GitCommit: fake.Commit,
		GitTag: tag, Measurement: fake.Measurement(8), CommittedAt: fake.CommittedAt,
	}
	revoked := revocationStatement{
		Version: RevocationVersion, Repository: record.Repository, GitCommit: record.GitCommit,
		GitTag: record.GitTag, Measurement: record.Measurement, Reason: "kernel CVE", RevokedAt: "2026-11-01T00:00:00Z",
	}
	item := func(p proof, revokedFlag bool, revocation *proof) map[string]any {
		m := map[string]any{"statement": record, "proof": p, "revoked": revokedFlag}
		if revocation != nil {
			m["revocation"] = map[string]any{"proof": revocation}
		}
		return m
	}
	good := sign(t, key, record)
	badSig := good
	badSig.Signature = sign(t, key, revoked).Signature
	validRevocation := sign(t, key, revoked)
	untrustedRevocation := sign(t, other, revoked)
	for _, tc := range []struct {
		name        string
		items       []map[string]any
		status      int
		wantFailure string
		wantErr     string
	}{
		{"not revoked", []map[string]any{item(good, false, nil)}, 200, "", ""},
		{"revoked", []map[string]any{item(good, true, &validRevocation)}, 200, "revoked: kernel CVE", ""},
		{"revoked by an untrusted key", []map[string]any{item(good, true, &untrustedRevocation)}, 200, "not signed by a trusted", ""},
		{"revoked flag only", []map[string]any{item(good, true, nil)}, 200, "not signed by a trusted", ""},
		{"invalid trusted record", []map[string]any{item(badSig, false, nil)}, 200, "invalid Release Key signature", ""},
		{"untrusted records only", []map[string]any{item(sign(t, other, record), true, nil)}, 200, "", "trusted Release Key"},
		{"empty", nil, 200, "", "trusted Release Key"},
		{"unavailable", nil, 503, "", "HTTP 503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/build-records/by-measurement/"+record.Measurement {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"items": tc.items, "count": len(tc.items)})
			}))
			defer server.Close()
			failure, err := CheckRegistry(t.Context(), network.Client(false), server.URL, record.Measurement, public(key))
			if !strings.Contains(failure, tc.wantFailure) || tc.wantFailure == "" && failure != "" {
				t.Fatalf("failure %q, want %q", failure, tc.wantFailure)
			}
			if (tc.wantErr == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err %v, want %q", err, tc.wantErr)
			}
		})
	}
}
