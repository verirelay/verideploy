package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/verirelay/protocol/avr1"

	"github.com/verirelay/deploy/internal/network"
)

const (
	BuildRecordVersion = "verirelay-build-record/1"
	RevocationVersion  = "verirelay-revocation/1"
	maxRegistryAnswer  = 8 << 20
)

// Statement is a Build Record statement. Its fields are in canonical order,
// so json.Marshal yields the canonical bytes the Release Key signs (Registry
// docs/api.md, "Canonical bytes").
type Statement struct {
	Version     string `json:"version"`
	Repository  string `json:"repository"`
	GitCommit   string `json:"git_commit"`
	GitTag      string `json:"git_tag"`
	Measurement string `json:"measurement"`
	CommittedAt string `json:"committed_at"`
}

// KeyID returns the Registry key_id of a Release Key.
func KeyID(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// VerifyBuildRecords returns the Build Record in data, a build-records.json
// release asset, that binds measurement to tag and repository under one of
// keys, and the key_id of that key. A decoded value that differs from the
// signed one changes the canonical bytes, so the signature check also covers
// decoding.
func VerifyBuildRecords(data []byte, measurement, tag, repository string, keys []ed25519.PublicKey) (Statement, string, error) {
	var records []struct {
		Statement Statement `json:"statement"`
		Signature string    `json:"signature"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&records); err != nil {
		return Statement{}, "", fmt.Errorf("build-records.json: %w", err)
	}
	found, signed := false, false
	for _, r := range records {
		if r.Statement.Measurement != measurement {
			continue
		}
		found = true
		payload, err := json.Marshal(r.Statement)
		if err != nil {
			return Statement{}, "", err
		}
		sig, err := avr1.DecodeBase64URL(r.Signature)
		if err != nil {
			continue
		}
		for _, key := range keys {
			if !ed25519.Verify(key, payload, sig) {
				continue
			}
			signed = true
			if r.Statement.Version == BuildRecordVersion && r.Statement.GitTag == tag && r.Statement.Repository == repository {
				return r.Statement, KeyID(key), nil
			}
		}
	}
	switch {
	case !found:
		return Statement{}, "", fmt.Errorf("build-records.json has no Build Record for measurement %s", measurement)
	case !signed:
		return Statement{}, "", errors.New("no Build Record for this measurement is signed by a trusted Release Key")
	default:
		return Statement{}, "", fmt.Errorf("the signed Build Record for this measurement does not name release %s of %s", tag, repository)
	}
}

type proof struct {
	Algorithm     string `json:"algorithm"`
	KeyID         string `json:"key_id"`
	SignedPayload string `json:"signed_payload"`
	Signature     string `json:"signature"`
}

type revocationStatement struct {
	Version     string `json:"version"`
	Repository  string `json:"repository"`
	GitCommit   string `json:"git_commit"`
	GitTag      string `json:"git_tag"`
	Measurement string `json:"measurement"`
	Reason      string `json:"reason"`
	RevokedAt   string `json:"revoked_at"`
}

// CheckRegistry asks Registry at base about measurement and judges the answer
// as the Verifier's code verdict does, so Deploy never launches a measurement
// whose Receipts that verdict fails. failure describes a Build Record under
// one of keys that is revoked or carries an invalid signature. err reports an
// answer that leaves the verdict undecided, such as an unreachable Registry;
// callers warn and go on.
func CheckRegistry(ctx context.Context, c *http.Client, base, measurement string, keys []ed25519.PublicKey) (failure string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := network.Get(ctx, c, base+"/v1/build-records/by-measurement/"+measurement, maxRegistryAnswer)
	if err != nil {
		return "", fmt.Errorf("lookup in Registry failed: %w", err)
	}
	var answer struct {
		Items []struct {
			Proof      proof `json:"proof"`
			Revoked    bool  `json:"revoked"`
			Revocation *struct {
				Proof proof `json:"proof"`
			} `json:"revocation"`
		} `json:"items"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(data, &answer); err != nil || answer.Count != len(answer.Items) {
		return "", errors.New("the answer from Registry is not a valid lookup result")
	}
	trusted := false
	for _, item := range answer.Items {
		key := trustedKey(item.Proof.KeyID, keys)
		if key == nil {
			continue
		}
		var s Statement
		if !verifyProof(item.Proof, key, &s) || s.Version != BuildRecordVersion {
			return "Registry holds a Build Record for this measurement with an invalid Release Key signature", nil
		}
		if s.Measurement != measurement {
			return "", errors.New("the answer from Registry names another measurement")
		}
		trusted = true
		if item.Revoked || item.Revocation != nil {
			reason := "its revocation is not signed by a trusted Release Key"
			if item.Revocation != nil {
				if r, ok := revocation(item.Revocation.Proof, s, keys); ok {
					reason = r
				}
			}
			return fmt.Sprintf("Registry marks release %s of %s revoked: %s", s.GitTag, s.Repository, reason), nil
		}
	}
	if !trusted {
		return "", errors.New("no Build Record for this measurement in Registry is signed by a trusted Release Key")
	}
	return "", nil
}

// revocation returns the reason of a revocation of s signed by one of keys.
func revocation(p proof, s Statement, keys []ed25519.PublicKey) (string, bool) {
	key := trustedKey(p.KeyID, keys)
	var r revocationStatement
	if key == nil || !verifyProof(p, key, &r) || r.Version != RevocationVersion {
		return "", false
	}
	same := r.Repository == s.Repository && r.GitCommit == s.GitCommit && r.GitTag == s.GitTag && r.Measurement == s.Measurement
	return r.Reason, same
}

func trustedKey(id string, keys []ed25519.PublicKey) ed25519.PublicKey {
	for _, key := range keys {
		if KeyID(key) == id {
			return key
		}
	}
	return nil
}

// verifyProof checks p with key and decodes the signed payload strictly into
// v, so every field read afterwards is signed.
func verifyProof(p proof, key ed25519.PublicKey, v any) bool {
	payload, err := avr1.DecodeBase64URL(p.SignedPayload)
	if err != nil || p.Algorithm != "ed25519" {
		return false
	}
	sig, err := avr1.DecodeBase64URL(p.Signature)
	if err != nil || !ed25519.Verify(key, payload, sig) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v) == nil
}
