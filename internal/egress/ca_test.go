package egress

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSessionAuthorityExportsOnlyItsPublicCertificate(t *testing.T) {
	now := time.Now()
	a, err := newSessionAuthority(now)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(a.publicPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("authority export is not exactly one public certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || !cert.MaxPathLenZero {
		t.Fatalf("invalid CA constraints: %v", err)
	}
	key, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		t.Fatal("CA does not use ECDSA P-256")
	}
	wire, err := json.Marshal(InitResult{CACert: string(a.publicPEM)})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal(wire, &fields); err != nil || len(fields) != 1 || fields["ca_cert"] != string(a.publicPEM) {
		t.Fatalf("init result exposed fields beyond the public CA: %s, %v", wire, err)
	}
	if bytes.Contains(wire, []byte("PRIVATE KEY")) {
		t.Fatal("private key entered public init result")
	}
	other, err := newSessionAuthority(now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(other.cert.Raw, a.cert.Raw) || other.key.Equal(a.key) {
		t.Fatal("two sessions reused an authority")
	}
}

func TestSessionAuthorityLeafIdentityExpiryAndBoundedEviction(t *testing.T) {
	now := time.Now()
	a, err := newSessionAuthority(now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.certificate("api.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(a.cert)
	if _, err := first.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "api.example.com", CurrentTime: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "other.example.com", CurrentTime: now}); err == nil {
		t.Fatal("leaf authenticates an unrelated destination")
	}
	if got, _ := a.certificate("api.example.com", now); got != first {
		t.Fatal("live destination leaf was not cached")
	}
	fresh, err := a.certificate("api.example.com", now.Add(25*time.Hour))
	if err != nil || fresh == first || !bytes.Equal(fresh.Certificate[1], a.cert.Raw) {
		t.Fatalf("expired leaf did not refresh under the same CA: %v", err)
	}
	for i := range maxCachedLeaves + 1 {
		if _, err := a.certificate(fmt.Sprintf("host%d.example.com", i), now); err != nil {
			t.Fatalf("cache refused destination %d: %v", i, err)
		}
	}
	if len(a.leaves) != maxCachedLeaves || len(a.order) != maxCachedLeaves {
		t.Fatalf("leaf cache grew without bound: %d, %d", len(a.leaves), len(a.order))
	}
	if _, err := a.certificate("api.example.com", a.cert.NotAfter); err == nil {
		t.Fatal("expired authority continued signing")
	}
	for _, invalid := range []string{"", "API.example.com", "*.example.com", "api.example.com:443"} {
		if _, err := a.certificate(invalid, now); err == nil {
			t.Fatalf("noncanonical certificate destination accepted: %q", invalid)
		}
	}
}

func TestSessionAuthorityConcurrentLeafIssuanceIsBounded(t *testing.T) {
	now := time.Now()
	a, err := newSessionAuthority(now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			if _, err := a.certificate(fmt.Sprintf("host%d.example.com", i), now); err != nil {
				t.Errorf("issue leaf: %v", err)
			}
		})
	}
	wg.Wait()
	if len(a.leaves) != 32 {
		t.Fatalf("concurrent cache entries = %d", len(a.leaves))
	}
}
