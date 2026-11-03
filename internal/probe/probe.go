// Package probe asks a running instance whether it is ready and what it
// attests, and explains a startup failure from its journal.
package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/verirelay/protocol/avr1"

	"github.com/verirelay/verideploy/internal/network"
	"github.com/verirelay/verideploy/internal/plan"
)

// Field offsets in the AMD SEV-SNP ATTESTATION_REPORT.
const (
	reportDataOffset  = 0x50
	measurementOffset = 0x90
	hostDataOffset    = 0xC0
	hostDataEnd       = 0xE0
)

// BaseURL returns the HTTP origin of an instance's port forward, reached over
// loopback when the forward listens on every address.
func BaseURL(forward string) string {
	ap, err := netip.ParseAddrPort(forward)
	if err == nil && ap.Addr().IsUnspecified() {
		forward = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), ap.Port()).String()
	}
	return "http://" + forward
}

// Ready returns nil when base answers /readyz with 200.
func Ready(ctx context.Context, c *http.Client, base string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := network.Get(ctx, c, base+"/readyz", 1<<10)
	return err
}

// WaitReady polls Ready every interval until it succeeds or ctx ends.
func WaitReady(ctx context.Context, c *http.Client, base string, interval time.Duration) error {
	for {
		err := Ready(ctx, c, base)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s/readyz: %w (last: %w)", base, ctx.Err(), err)
		case <-time.After(interval):
		}
	}
}

// Attestation is what a fresh attestation response states.
type Attestation struct {
	// Bound reports whether REPORT_DATA binds the Receipt Signing Key to the
	// nonce of this request.
	Bound       bool
	Measurement string
	HostData    string
	// Bootstrap is the disclosed Bootstrap, nil in development mode.
	Bootstrap []byte
}

// Attest requests an attestation with a random nonce. It reads the report's
// fields without verifying its AMD signature: VeriProv did that before
// provisioning, and the Verifier does it for customers.
func Attest(ctx context.Context, c *http.Client, base string) (Attestation, error) {
	nonce := make([]byte, avr1.AVRNonceBytes)
	_, _ = rand.Read(nonce)
	encoded := base64.RawURLEncoding.EncodeToString(nonce)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := network.Get(ctx, c, base+"/v1/avr/attestation?nonce="+encoded, 1<<20)
	if err != nil {
		return Attestation{}, fmt.Errorf("attestation: %w", err)
	}
	r, err := avr1.DecodeAttestationResponse(data)
	if err != nil {
		return Attestation{}, fmt.Errorf("attestation: %w", err)
	}
	if r.Nonce != encoded {
		return Attestation{}, fmt.Errorf("attestation answers nonce %q, not the one sent", r.Nonce)
	}
	report, err := avr1.DecodeBase64URL(r.Evidence.Report)
	if err != nil || len(report) != avr1.AttestationReportSize {
		return Attestation{}, fmt.Errorf("attestation report is not %d bytes of base64url", avr1.AttestationReportSize)
	}
	key, err := avr1.DecodeEd25519PublicKeyBase64URL(r.ReceiptSigningPublicKey)
	if err != nil {
		return Attestation{}, fmt.Errorf("attestation: %w", err)
	}
	want, err := avr1.ReportDataAVR1(key, nonce)
	if err != nil {
		return Attestation{}, err
	}
	a := Attestation{
		Bound:       bytes.Equal(report[reportDataOffset:measurementOffset], want[:]),
		Measurement: hex.EncodeToString(report[measurementOffset:hostDataOffset]),
		HostData:    hex.EncodeToString(report[hostDataOffset:hostDataEnd]),
	}
	if r.Bootstrap != "" {
		if a.Bootstrap, err = avr1.DecodeBase64URL(r.Bootstrap); err != nil {
			return Attestation{}, fmt.Errorf("attestation bootstrap: %w", err)
		}
	}
	return a, nil
}

type rule struct {
	// match lists alternatives; a line matches one when it contains all of
	// its strings.
	match [][]string
	cause func(plan.Plan) string
}

var gateOpened = rule{[][]string{{"server installed; readiness gate opened"}}, func(plan.Plan) string {
	return "Provisioning succeeded, but /readyz stays 503: the CVM cannot reach the Receipt or attestation store set in the Runtime Config."
}}

// rules are in priority order within one line. VeriRelay wraps every
// provisioning error in "provisioning unavailable", so the specific causes
// come before it.
var rules = []rule{
	{[][]string{{"policy.denied"}}, func(p plan.Plan) string {
		return fmt.Sprintf("VeriProv refused this CVM: its Attestation policy lacks measurement %s, or none of its deployments has HOST_DATA %s. VeriProv's log names which.", p.Measurement, p.HostData)
	}},
	{[][]string{{"bootstrap does not match HOST_DATA"}}, func(p plan.Plan) string {
		return fmt.Sprintf("%s and the unit's host-data disagree. Run verideploy up again.", p.ConfigDisk)
	}},
	{[][]string{{"provision envelope signature rejected"}}, func(p plan.Plan) string {
		return fmt.Sprintf("VeriProv at %s signs with a key other than the Bootstrap's identity_public_key: the Bootstrap was issued by another VeriProv.", p.VeriProv)
	}},
	{[][]string{{"invalid bootstrap"}, {"want exactly one virtio block device"}}, func(p plan.Plan) string {
		return fmt.Sprintf("The CVM cannot read the Bootstrap from %s.", p.ConfigDisk)
	}},
	{[][]string{{"provisioning unavailable", "x509:"}}, func(p plan.Plan) string {
		return fmt.Sprintf("The CVM does not trust the TLS certificate of %s.", p.VeriProv)
	}},
	{[][]string{{"provisioning unavailable"}}, func(p plan.Plan) string {
		return fmt.Sprintf("The CVM cannot reach VeriProv at %s.", p.VeriProv)
	}},
	{[][]string{{"fetch AMD KDS resource"}}, func(plan.Plan) string {
		return "The CVM cannot fetch its VCEK from AMD KDS (kdsintf.amd.com)."
	}},
}

// Diagnose returns the likely cause of a startup failure and the journal line
// it is based on, or "" when the journal shows no known failure. An opened
// readiness gate means provisioning worked, so it outranks every failed
// attempt; otherwise the most recent known failure counts.
func Diagnose(journal []byte, p plan.Plan) string {
	lines := strings.Split(string(journal), "\n")
	for _, line := range lines {
		if gateOpened.matches(line) {
			return explain(gateOpened, p, line)
		}
	}
	qemu := rule{[][]string{{filepath.Base(p.Argv[0]) + ":"}}, func(plan.Plan) string { return "QEMU failed to start the CVM." }}
	for _, line := range slices.Backward(lines) {
		for _, r := range slices.Concat(rules, []rule{qemu}) {
			if r.matches(line) {
				return explain(r, p, line)
			}
		}
	}
	return ""
}

func (r rule) matches(line string) bool {
	return slices.ContainsFunc(r.match, func(all []string) bool {
		return !slices.ContainsFunc(all, func(s string) bool { return !strings.Contains(line, s) })
	})
}

func explain(r rule, p plan.Plan, line string) string {
	return r.cause(p) + "\n  " + clip(strings.TrimSpace(line), 400)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
