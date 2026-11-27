package supervisor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func publicSessionCAFixture(t *testing.T, authority bool) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: authority, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Unix(1, 0), NotAfter: time.Unix(2, 0)}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestPublicSessionCertificateRejectsEveryNoncertificateCarrier(t *testing.T) {
	certificate := publicSessionCAFixture(t, true)
	if err := validatePublicSessionCA(certificate); err != nil {
		t.Fatal("actual-format public CA was refused")
	}
	for _, raw := range []string{"", "private-value-marker\n" + certificate, certificate + "private-value-marker", certificate + certificate,
		strings.Replace(certificate, "CERTIFICATE", "PRIVATE KEY", -1), publicSessionCAFixture(t, false),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Comment": "private-value-marker"}, Bytes: []byte("not-a-certificate")}))} {
		if err := validatePublicSessionCA(raw); err == nil || strings.Contains(err.Error(), "private-value-marker") {
			t.Fatal("noncertificate data was accepted or echoed")
		}
	}
}
