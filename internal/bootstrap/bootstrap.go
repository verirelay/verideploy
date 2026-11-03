// Package bootstrap fetches the Bootstrap of an instance from its VeriProv.
package bootstrap

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/verirelay/protocol/avr1"
	"github.com/verirelay/protocol/guest"

	"github.com/verirelay/verideploy/internal/network"
)

// Entry is one Bootstrap VeriProv serves at GET /v1/bootstraps.
type Entry struct {
	Profile   string `json:"profile"`
	HostData  string `json:"host_data"`
	Bootstrap string `json:"bootstrap"`
}

// List returns the Bootstraps the VeriProv at origin serves.
func List(ctx context.Context, c *http.Client, origin string) ([]Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := network.Get(ctx, c, origin+"/v1/bootstraps", 1<<20)
	// VeriProv before v0.2.0 answers an unknown route with 400.
	var status *network.StatusError
	if errors.As(err, &status) && (status.Status == http.StatusBadRequest || status.Status == http.StatusNotFound) {
		return nil, fmt.Errorf("VeriProv at %s does not serve /v1/bootstraps; VeriProv v0.2.0 and later do", origin)
	}
	if err != nil {
		return nil, fmt.Errorf("%s/v1/bootstraps: %w", origin, err)
	}
	var answer struct {
		Bootstraps []Entry `json:"bootstraps"`
	}
	if err := json.Unmarshal(data, &answer); err != nil || len(answer.Bootstraps) == 0 {
		return nil, fmt.Errorf("%s/v1/bootstraps did not answer with a list of Bootstraps", origin)
	}
	return answer.Bootstraps, nil
}

// Profiles names the profiles of entries; the empty profile reads as "".
func Profiles(entries []Entry) string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = fmt.Sprintf("%q", e.Profile)
	}
	return strings.Join(names, ", ")
}

// Select returns the exact bytes of the Bootstrap for profile in entries,
// after checking that they hash to the HOST_DATA served with them, decode as
// a valid Bootstrap, and name origin and profile.
func Select(entries []Entry, origin, profile string) ([]byte, error) {
	i := slices.IndexFunc(entries, func(e Entry) bool { return e.Profile == profile })
	if i < 0 {
		return nil, fmt.Errorf("VeriProv at %s has no Bootstrap with profile %q; it has %s", origin, profile, Profiles(entries))
	}
	data, err := avr1.DecodeBase64URL(entries[i].Bootstrap)
	if err != nil {
		return nil, fmt.Errorf("the Bootstrap with profile %q is not base64url: %w", profile, err)
	}
	hostData := guest.HostData(data)
	if hex.EncodeToString(hostData[:]) != entries[i].HostData {
		return nil, fmt.Errorf("the Bootstrap with profile %q does not hash to the HOST_DATA served with it", profile)
	}
	b, err := guest.DecodeBootstrap(data)
	if err == nil {
		err = b.Validate()
	}
	if err != nil {
		return nil, fmt.Errorf("the Bootstrap with profile %q is invalid: %w", profile, err)
	}
	if b.URL != origin || b.Profile != profile {
		return nil, fmt.Errorf("the Bootstrap served names VeriProv %s and profile %q, not %s and %q", b.URL, b.Profile, origin, profile)
	}
	return data, nil
}

// Fetch returns the exact bytes of the Bootstrap for profile from the
// VeriProv at origin.
func Fetch(ctx context.Context, c *http.Client, origin, profile string) ([]byte, error) {
	entries, err := List(ctx, c, origin)
	if err != nil {
		return nil, err
	}
	return Select(entries, origin, profile)
}
