package client

import (
	"bytes"
	"crypto/dsa"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"testing"

	"golang.org/x/crypto/ssh"
)

func agentCertificateSigners(t *testing.T) []ssh.Signer {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dsaKey := new(dsa.PrivateKey)
	if err := dsa.GenerateParameters(&dsaKey.Parameters, rand.Reader, dsa.L1024N160); err != nil {
		t.Fatal(err)
	}
	if err := dsa.GenerateKey(dsaKey, rand.Reader); err != nil {
		t.Fatal(err)
	}
	keys := []any{rsaKey, edKey, dsaKey}
	for _, curve := range []elliptic.Curve{elliptic.P256(), elliptic.P384(), elliptic.P521()} {
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	var signers []ssh.Signer
	for _, key := range keys {
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			t.Fatal(err)
		}
		signers = append(signers, signer)
	}
	return signers
}

func certificateProjectionOracle(t *testing.T, blob []byte) {
	t.Helper()
	full, fullError := ssh.ParsePublicKey(blob)
	projected, kind, projectionError := parseAgentBindingKey(blob)
	if (fullError == nil) != (projectionError == nil) {
		t.Fatalf("parser acceptance differs: library=%v projection=%v", fullError, projectionError)
	}
	if fullError != nil {
		return
	}
	leaf := full
	if certificate, ok := full.(*ssh.Certificate); ok {
		leaf = certificate.Key
	}
	if kind != full.Type() || !bytes.Equal(projected.Marshal(), leaf.Marshal()) {
		t.Fatalf("projection changed exact key type or leaf: type=%s, want %s", kind, full.Type())
	}
}

func TestCertificateProjectionAllCurrentSoftwareAlgorithms(t *testing.T) {
	signers := agentCertificateSigners(t)
	for _, signer := range signers {
		t.Run(signer.PublicKey().Type(), func(t *testing.T) {
			certificateProjectionOracle(t, signer.PublicKey().Marshal())
			certificate := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.HostCert,
				KeyId: "generated-public-fixture", ValidPrincipals: []string{"example.invalid"},
				ValidAfter: 1, ValidBefore: ssh.CertTimeInfinity}
			if err := certificate.SignCert(rand.Reader, signers[1]); err != nil {
				t.Fatal(err)
			}
			blob := certificate.Marshal()
			certificateProjectionOracle(t, blob)
			projected, _, err := parseAgentBindingKey(blob)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("generated binding SID")
			signature, err := signer.Sign(rand.Reader, data)
			if err != nil {
				t.Fatal(err)
			}
			if err := projected.Verify(data, signature); err != nil {
				t.Fatalf("same library primitive lost a valid SID signature: %v", err)
			}
			for n := range len(blob) {
				certificateProjectionOracle(t, blob[:n])
			}
			certificateProjectionOracle(t, append(bytes.Clone(blob), 0))
		})
	}
}

type agentCertificateTailFixture struct {
	Serial          uint64
	CertType        uint32
	KeyID           string
	Principals      []byte
	ValidAfter      uint64
	ValidBefore     uint64
	CriticalOptions []byte
	Extensions      []byte
	Reserved        []byte
	SignatureKey    []byte
	Signature       []byte
}

func agentCertificateString(value []byte) []byte {
	out := make([]byte, 4+len(value))
	binary.BigEndian.PutUint32(out[:4], uint32(len(value)))
	copy(out[4:], value)
	return out
}

func agentCertificateFixtureBlob(key ssh.PublicKey, tail agentCertificateTailFixture) []byte {
	plainType, leaf, ok := takeAgentFirewallString(key.Marshal())
	if !ok {
		panic("generated public key has no wire type")
	}
	var certificateType string
	switch string(plainType) {
	case ssh.KeyAlgoED25519:
		certificateType = ssh.CertAlgoED25519v01
	case ssh.KeyAlgoSKECDSA256:
		certificateType = ssh.CertAlgoSKECDSA256v01
	case ssh.KeyAlgoSKED25519:
		certificateType = ssh.CertAlgoSKED25519v01
	default:
		panic("unexpected fixture key type")
	}
	out := agentCertificateString([]byte(certificateType))
	out = append(out, agentCertificateString([]byte("generated nonce"))...)
	out = append(out, leaf...)
	return append(out, ssh.Marshal(tail)...)
}

func TestCertificateProjectionMatchesAllMetadataRejections(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	base := agentCertificateTailFixture{CertType: ssh.HostCert, ValidBefore: ssh.CertTimeInfinity,
		SignatureKey: signer.PublicKey().Marshal(), Signature: ssh.Marshal(&ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: []byte("structural-only CA signature")})}
	tuple := func(name, value string) []byte {
		out := agentCertificateString([]byte(name))
		return append(out, agentCertificateString([]byte(value))...)
	}
	for _, change := range []string{"valid", "user type", "unknown type", "bad principal", "duplicate option", "reverse option", "nested extra", "nested short", "empty key",
		"bad CA", "certificate CA", "signature alias CA", "CA trailing", "CA signature trailing", "SK CA signature rest", "reserved bytes"} {
		t.Run(change, func(t *testing.T) {
			tail := base
			switch change {
			case "user type":
				tail.CertType = ssh.UserCert // Full parser accepts it. Native host provenance remains separate.
			case "unknown type":
				tail.CertType = ^uint32(0)
			case "bad principal":
				tail.Principals = []byte{0, 0, 0, 1}
			case "duplicate option":
				tail.Extensions = append(tuple("a", ""), tuple("a", "")...)
			case "reverse option":
				tail.CriticalOptions = append(tuple("b", ""), tuple("a", "")...)
			case "nested extra":
				tail.Extensions = tuple("a", string(append(agentCertificateString(nil), 1)))
			case "nested short":
				tail.Extensions = tuple("a", "x")
			case "empty key":
				tail.Extensions = tuple("", "")
			case "bad CA":
				tail.SignatureKey = []byte{0}
			case "certificate CA":
				tail.SignatureKey = agentCertificateFixtureBlob(signer.PublicKey(), base)
			case "signature alias CA":
				tail.SignatureKey = agentCertificateString([]byte(ssh.CertAlgoRSASHA256v01))
			case "CA trailing":
				tail.SignatureKey = append(bytes.Clone(base.SignatureKey), 0)
			case "CA signature trailing":
				tail.Signature = append(bytes.Clone(base.Signature), 0)
			case "SK CA signature rest":
				tail.Signature = ssh.Marshal(&ssh.Signature{Format: ssh.KeyAlgoSKED25519, Blob: []byte{1}, Rest: []byte{1, 2, 3}})
			case "reserved bytes":
				tail.Reserved = []byte{0, 1, 0xff}
			}
			certificateProjectionOracle(t, agentCertificateFixtureBlob(signer.PublicKey(), tail))
		})
	}
	// The full parser permits empty and binary option names, but still
	// requires strict byte-wise order and exactly nested nonempty values.
	tail := base
	tail.Extensions = append(tuple("", ""), tuple("\xff", string(agentCertificateString(nil)))...)
	certificateProjectionOracle(t, agentCertificateFixtureBlob(signer.PublicKey(), tail))
}

func TestCertificateProjectionRejectsSignatureOnlyWireNamesLikeTheLibrary(t *testing.T) {
	for _, algorithm := range []string{ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSASHA512v01, "unknown-cert-v01@openssh.com"} {
		certificateProjectionOracle(t, agentCertificateString([]byte(algorithm)))
	}
	for _, algorithm := range []string{ssh.CertAlgoMLDSA44v01Go, ssh.CertAlgoMLDSA65v01Go, ssh.CertAlgoMLDSA87v01Go} {
		// One malformed public-key byte is rejected on supported builds and
		// unsupported by the same library on Go1.26. No algorithm fallback.
		blob := agentCertificateString([]byte(algorithm))
		blob = append(blob, agentCertificateString(nil)...)
		blob = append(blob, agentCertificateString([]byte{1})...)
		certificateProjectionOracle(t, blob)
	}
}
