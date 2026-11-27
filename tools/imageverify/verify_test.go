package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/imageverify"
)

func recordedSignature(t *testing.T) ([]byte, []byte) {
	t.Helper()
	bundle, err := os.ReadFile("testdata/signature-v0.3.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile("testdata/trusted-root.json")
	if err != nil {
		t.Fatal(err)
	}
	return bundle, root
}

func TestRecordedCosignOCIBundleBindsImageDigest(t *testing.T) {
	bundle, err := os.ReadFile("testdata/cosign-oci-v0.3.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile("testdata/cosign-trusted-root.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:732112270d7e59418a8c080b134b24cabd67d250d0d0147a97ed95ba5c280aa4"
	var recorded struct {
		VerificationMaterial struct {
			Certificate struct {
				RawBytes []byte `json:"rawBytes"`
			} `json:"certificate"`
		} `json:"verificationMaterial"`
	}
	if err := json.Unmarshal(bundle, &recorded); err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(recorded.VerificationMaterial.Certificate.RawBytes)
	if err != nil || len(certificate.EmailAddresses) != 1 {
		t.Fatalf("recorded certificate identity: %v", err)
	}
	identity := certificate.EmailAddresses[0]
	if err := verifyImageSignature(context.Background(), bundle, nil, digest, identity, "https://github.com/login/oauth", root); err != nil {
		t.Fatal(err)
	}
	if err := verifyImageSignature(context.Background(), bundle, nil, "sha256:"+strings.Repeat("a", 64), identity, "https://github.com/login/oauth", root); err == nil {
		t.Fatal("cosign OCI bundle covered another image")
	}
	if err := verifyImageSignature(context.Background(), bundle, nil, digest, "other@example.com", "https://github.com/login/oauth", root); err == nil {
		t.Fatal("cosign OCI bundle accepted another certificate identity")
	}
}

func TestRecordedSignatureRequiresExactDigestIdentityIssuerAndLog(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = refuseNetworkTransport{}
	t.Cleanup(func() { http.DefaultTransport = previous })
	bundle, root := recordedSignature(t)
	digest := "sha256:bc103b4a84971ef6459b294a2b98568a2bfb72cded09d4acd1e16366a401f95b"
	for _, row := range []struct {
		name, digest, identity, issuer string
		bundle, root                   []byte
		ok                             bool
	}{
		{"valid", digest, "foo!oidc.local", "http://oidc.local:8080", bundle, root, true},
		{"digest", "sha256:" + strings.Repeat("0", 64), "foo!oidc.local", "http://oidc.local:8080", bundle, root, false},
		{"identity", digest, "other@example.com", "http://oidc.local:8080", bundle, root, false},
		{"issuer", digest, "foo!oidc.local", "http://other.local:8080", bundle, root, false},
		{"root", digest, "foo!oidc.local", "http://oidc.local:8080", bundle, []byte(`{}`), false},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := verifyImageSignature(context.Background(), row.bundle, nil, row.digest, row.identity, row.issuer, row.root)
			if (err == nil) != row.ok {
				t.Fatalf("verify: %v", err)
			}
		})
	}
	var missing map[string]any
	json.Unmarshal(bundle, &missing)
	missing["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{}
	withoutLog, _ := json.Marshal(missing)
	if verifyImageSignature(context.Background(), withoutLog, nil, digest, "foo!oidc.local", "http://oidc.local:8080", root) == nil {
		t.Fatal("signature without transparency entry passed")
	}
}

type refuseNetworkTransport struct{}

func (refuseNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("offline image verification attempted a network request")
}

func TestVerifierEmitsBoundResponseOnlyAfterRealVerification(t *testing.T) {
	bundle, root := recordedSignature(t)
	request := imageverify.RequestV1{Version: imageverify.Version, Role: imageverify.Role, Nonce: strings.Repeat("a", 64), Digest: "sha256:bc103b4a84971ef6459b294a2b98568a2bfb72cded09d4acd1e16366a401f95b", Identity: "foo!oidc.local", Issuer: "http://oidc.local:8080", TrustedRoot: root, Bundle: bundle}
	wire, _ := json.Marshal(request)
	var output bytes.Buffer
	if err := run(bytes.NewReader(wire), &output); err != nil {
		t.Fatal(err)
	}
	if err := imageverify.CheckResponse(output.Bytes(), Version, wire); err != nil {
		t.Fatal(err)
	}
	request.Identity = "attacker"
	wire, _ = json.Marshal(request)
	output.Reset()
	if err := run(bytes.NewReader(wire), &output); err == nil || output.Len() != 0 {
		t.Fatal("failed verification emitted success")
	}
}
