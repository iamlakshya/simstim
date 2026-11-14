package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"

	"golang.org/x/crypto/ssh"
)

func denseCertificateFixture(t testing.TB, dense bool) []byte {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.HostCert, ValidBefore: ssh.CertTimeInfinity,
		Permissions: ssh.Permissions{Extensions: map[string]string{}}}
	if dense {
		for i := range 12_000 {
			certificate.Extensions[fmt.Sprintf("e%05d", i)] = ""
		}
	}
	if err := certificate.SignCert(rand.Reader, signer); err != nil {
		t.Fatal(err)
	}
	// Pad with one sorted last extension to an exact maximum public-key
	// blob. The full binding frame has its separate enclosing body bound.
	base := len(certificate.Marshal())
	padding := agentFWMaxBody - base - len("zz-padding") - 12
	if padding < 0 {
		t.Fatal("dense fixture exceeds its declared input range")
	}
	certificate.Extensions["zz-padding"] = string(bytes.Repeat([]byte{'x'}, padding))
	if err := certificate.SignCert(rand.Reader, signer); err != nil {
		t.Fatal(err)
	}
	blob := certificate.Marshal()
	if len(blob) != agentFWMaxBody {
		t.Fatalf("maximum fixture size=%d, want %d", len(blob), agentFWMaxBody)
	}
	return blob
}

func TestCertificateProjectionMaximumFlatAndDenseMetadata(t *testing.T) {
	for _, dense := range []bool{false, true} {
		t.Run(fmt.Sprint(dense), func(t *testing.T) {
			certificateProjectionOracle(t, denseCertificateFixture(t, dense))
		})
	}
}

func FuzzAgentCertificateProjection(f *testing.F) {
	f.Add(denseCertificateFixture(f, false))
	f.Add([]byte{0, 0, 0, 3, 'b', 'a', 'd'})
	f.Fuzz(func(t *testing.T, blob []byte) {
		if len(blob) > agentFWMaxBody {
			return
		}
		certificateProjectionOracle(t, blob)
	})
}

func BenchmarkAgentCertificateProjection(b *testing.B) {
	for _, dense := range []bool{false, true} {
		blob := denseCertificateFixture(b, dense)
		b.Run(fmt.Sprint(dense), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(blob)))
			for b.Loop() {
				if _, _, err := parseAgentBindingKey(blob); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
