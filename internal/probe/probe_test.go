package probe

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/verirelay/deploy/internal/fake"
	"github.com/verirelay/deploy/internal/network"
	"github.com/verirelay/deploy/internal/plan"
)

func TestAttest(t *testing.T) {
	cvm := &fake.CVM{Ready: true, Measurement: fake.Measurement(8), Bootstrap: []byte(fake.Bootstrap)}
	server := httptest.NewServer(cvm)
	defer server.Close()
	a, err := Attest(t.Context(), network.Client(false), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Bound || a.Measurement != fake.Measurement(8) || a.HostData != fake.BootstrapHostData || string(a.Bootstrap) != fake.Bootstrap {
		t.Fatalf("%+v", a)
	}
	wrong := httptest.NewServer(&fake.CVM{Measurement: fake.Measurement(8), WrongBinding: true})
	defer wrong.Close()
	if a, err := Attest(t.Context(), network.Client(false), wrong.URL); err != nil || a.Bound {
		t.Fatalf("wrong binding: %+v, %v", a, err)
	}
}

func TestWaitReady(t *testing.T) {
	cvm := &fake.CVM{}
	server := httptest.NewServer(cvm)
	defer server.Close()
	go func() {
		time.Sleep(30 * time.Millisecond)
		cvm.SetReady(true)
	}()
	if err := WaitReady(t.Context(), network.Client(false), server.URL, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestBaseURL(t *testing.T) {
	if got := BaseURL("0.0.0.0:18081"); got != "http://127.0.0.1:18081" {
		t.Fatal(got)
	}
	if got := BaseURL("10.0.0.5:18081"); got != "http://10.0.0.5:18081" {
		t.Fatal(got)
	}
}

func TestDiagnose(t *testing.T) {
	p := plan.Plan{
		Measurement: "72d1", HostData: "0433", VeriProv: "https://veriprov.example.com",
		ConfigDisk: "/srv/relay/config.img", Argv: []string{"/usr/bin/qemu-system-x86_64"},
	}
	failed := `{"level":"ERROR","msg":"startup failed; retrying","attempt":1,"err":"`
	for _, tc := range []struct{ name, journal, want string }{
		{"policy", failed + `provisioning unavailable: POST /v1/provision: HTTP 409 (policy.denied)"}`, "lacks measurement 72d1"},
		{"tls", failed + `provisioning unavailable: tls: failed to verify certificate: x509: certificate signed by unknown authority"}`, "TLS certificate of https://veriprov.example.com"},
		{"unreachable", failed + `provisioning unavailable: dial tcp: i/o timeout"}`, "cannot reach VeriProv"},
		{"config disk", failed + `want exactly one virtio block device in /dev, found 0 []"}`, "/srv/relay/config.img"},
		{"identity", failed + `provision envelope signature rejected"}`, "another VeriProv"},
		{"store", failed + "provisioning unavailable\"}\n" + `{"msg":"server installed; readiness gate opened","attempt":2}`, "store"},
		{"kds tls", failed + `initialize server: fetch AMD KDS resource: tls: failed to verify certificate: x509: unknown authority"}`, "AMD KDS"},
		{"latest attempt counts", failed + "provisioning unavailable: dial tcp: i/o timeout\"}\n" + failed + `initialize server: fetch AMD KDS resource: HTTP 503"}`, "AMD KDS"},
		{"mismatch", failed + `bootstrap does not match HOST_DATA"}`, "host-data disagree"},
		{"qemu", "qemu-system-x86_64: -object sev-snp-guest,id=sev0: SEV: guest firmware hashes table area is invalid", "QEMU failed"},
		{"unknown", `{"msg":"configuration loaded"}`, ""},
	} {
		got := Diagnose([]byte(tc.journal), p)
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
