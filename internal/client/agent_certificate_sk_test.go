package client

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"math/big"
	"testing"

	"golang.org/x/crypto/ssh"
)

// These software-generated protocol signatures test the existing verifier.
// A synthetic presence flag is not proof of a physical touch or device.
func TestCertificateProjectionPreservesSecurityKeyVerification(t *testing.T) {
	const application = "ssh:generated-fixture"
	message := []byte("generated security-key binding SID")
	appHash, messageHash := sha256.Sum256([]byte(application)), sha256.Sum256(message)
	flags := ssh.Marshal(struct {
		Flags   byte
		Counter uint32
	}{1, 7})
	authenticated := append(append(append([]byte(nil), appHash[:]...), flags...), messageHash[:]...)
	_, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(authenticated)
	r, s, err := ecdsa.Sign(rand.Reader, ecPrivate, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		algorithm string
		wire      []byte
		signature *ssh.Signature
	}{
		{ssh.KeyAlgoSKED25519, ssh.Marshal(struct {
			Name, Key, Application string
		}{ssh.KeyAlgoSKED25519, string(edPrivate.Public().(ed25519.PublicKey)), application}),
			&ssh.Signature{Format: ssh.KeyAlgoSKED25519, Blob: ed25519.Sign(edPrivate, authenticated), Rest: flags}},
		{ssh.KeyAlgoSKECDSA256, ssh.Marshal(struct {
			Name, Curve string
			Point       []byte
			Application string
		}{ssh.KeyAlgoSKECDSA256, "nistp256", elliptic.Marshal(elliptic.P256(), ecPrivate.X, ecPrivate.Y), application}),
			&ssh.Signature{Format: ssh.KeyAlgoSKECDSA256, Blob: ssh.Marshal(struct{ R, S *big.Int }{r, s}), Rest: flags}},
	}
	ca, err := ssh.NewSignerFromKey(edPrivate)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range cases {
		t.Run(row.algorithm, func(t *testing.T) {
			public, err := ssh.ParsePublicKey(row.wire)
			if err != nil {
				t.Fatal(err)
			}
			certificateProjectionOracle(t, row.wire)
			certificate := &ssh.Certificate{Key: public, CertType: ssh.HostCert, ValidBefore: ssh.CertTimeInfinity}
			if err := certificate.SignCert(rand.Reader, ca); err != nil {
				t.Fatal(err)
			}
			blob := certificate.Marshal()
			certificateProjectionOracle(t, blob)
			projected, _, err := parseAgentBindingKey(blob)
			if err != nil {
				t.Fatal(err)
			}
			if err := projected.Verify(message, row.signature); err != nil {
				t.Fatalf("valid software protocol signature changed: %v", err)
			}
			bad := *row.signature
			bad.Blob = append([]byte(nil), row.signature.Blob...)
			bad.Blob[len(bad.Blob)-1] ^= 1
			if projected.Verify(message, &bad) == nil || certificate.Verify(message, &bad) == nil {
				t.Fatal("a corrupted security-key signature was accepted")
			}
		})
	}
}
