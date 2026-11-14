//go:build darwin || linux

package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

type agentMemoryCertificate struct {
	wire                                        []byte
	keyID                                       string
	peer                                        agentNativePeer
	caType, caFingerprint                       string
	requireRefusal, allowRefusal, nativeGrammar bool
}

// Parent-generated fixtures are outside both measured actors. Hostile
// SDK-shape cases are not native-valid or native-provenance evidence.
func agentMemoryMaximumBinding(t *testing.T, shape string) agentMemoryCertificate {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ssh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if shape == "rsa-certificate" {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		signer, err = ssh.NewSignerFromKey(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	cert := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.HostCert, Serial: 1, KeyId: "memory-fixture", ValidPrincipals: []string{"memory.invalid"}, ValidBefore: ssh.CertTimeInfinity, Permissions: ssh.Permissions{Extensions: map[string]string{"~~~": ""}}}
	result := agentMemoryCertificate{nativeGrammar: true, caType: ca.PublicKey().Type(), caFingerprint: ssh.FingerprintSHA256(ca.PublicKey())}
	switch shape {
	case "flat-padding", "rsa-certificate":
	case "dense-short-names":
		if err := cert.SignCert(rand.Reader, ca); err != nil {
			t.Fatal(err)
		}
		base := agentMemoryBindingFrame(t, cert.Marshal(), signer)
		agentMemoryDenseExtensions(t, cert, agentFWMaxBody-(len(base)-4))
	case "hostile-principals":
		result.nativeGrammar = false
		result.allowRefusal = true
		// Native OpenSSH caps this list at256. This deliberately hostile
		// contract-peer input still must fit the same measured budget.
		for range 40000 {
			cert.ValidPrincipals = append(cert.ValidPrincipals, "p")
		}
	case "rsa-ca-maximum":
		result.nativeGrammar = false
	case "nested-ca-bomb":
		result.nativeGrammar = false
		result.requireRefusal = true
	default:
		t.Fatalf("unknown memory certificate shape %q", shape)
	}
	build := func(padding int) []byte {
		cert.Extensions["~~~"] = strings.Repeat("x", padding)
		if err := cert.SignCert(rand.Reader, ca); err != nil {
			t.Fatal(err)
		}
		blob := cert.Marshal()
		if shape == "rsa-ca-maximum" {
			// The pinned SDK permits16384 modulus bits and24 exponent bits.
			// This public key exercises that maximum parser shape. The CA
			// signature is intentionally unauthentic. No huge private key
			// or native OpenSSH validity is claimed for this resource case.
			public, err := ssh.NewPublicKey(&rsa.PublicKey{N: new(big.Int).SetBytes(bytes.Repeat([]byte{255}, 2048)), E: (1 << 24) - 1})
			if err != nil {
				t.Fatal(err)
			}
			cert.SignatureKey = public
			cert.Signature = &ssh.Signature{Format: ssh.KeyAlgoRSASHA512, Blob: make([]byte, 2048)}
			result.caType, result.caFingerprint = public.Type(), ssh.FingerprintSHA256(public)
			blob = cert.Marshal()
		}
		if shape == "nested-ca-bomb" {
			blob = agentMemoryNestedCertificate(t, cert, padding)
		}
		return agentMemoryBindingFrame(t, blob, signer)
	}
	plain := build(0)
	remaining := agentFWMaxBody - (len(plain) - 4)
	if remaining < 0 {
		t.Fatalf("maximum metadata fixture exceeds its body before padding: %d", len(plain))
	}
	// A nonempty option value has an additional inner SSH string prefix.
	// Small remainders lengthen the public certificate ID instead.
	padding := 0
	if remaining <= 4 {
		cert.KeyId += strings.Repeat("x", remaining)
	} else {
		padding = remaining - 4
	}
	result.wire = build(padding)
	result.keyID = cert.KeyId
	result.peer = agentNativePeer{version: "10.2", keyType: cert.Type(), fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), certificate: true}
	if len(result.wire) != agentFWMaxBody+4 {
		t.Fatalf("binding fixture has%d bytes", len(result.wire))
	}
	err = verifyNativeAgentRecord(result.wire, result.peer)
	if result.requireRefusal {
		if err == nil {
			t.Fatal("nested certificate CA was not refused by the actual verifier")
		}
	} else if !result.allowRefusal && err != nil {
		t.Fatal(err)
	}
	return result
}

func agentMemoryBindingFrame(t *testing.T, blob []byte, signer ssh.Signer) []byte {
	t.Helper()
	session := []byte("generated-maximum-memory-binding")
	signature, err := signer.Sign(rand.Reader, session)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte{agentFWExtension}
	for _, part := range [][]byte{[]byte(agentFWSessionBind), blob, session, ssh.Marshal(signature)} {
		body = binary.BigEndian.AppendUint32(body, uint32(len(part)))
		body = append(body, part...)
	}
	body = append(body, 1)
	wire := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	return append(wire, body...)
}

func agentMemoryDenseExtensions(t *testing.T, cert *ssh.Certificate, budget int) {
	t.Helper()
	// Native cert_parse accepts SSH string pairs. Printable ASCII bytes
	// avoid embedded NUL while yielding8836 shortest two-byte names. Fill
	// the rest with shortest three-byte names. Marshal sorts them by byte.
	used := 0
	count := 0
	for a := byte('!'); a <= '~'; a++ {
		for b := byte('!'); b <= '~'; b++ {
			name := string([]byte{a, b})
			cert.Extensions[name] = ""
			used += len(name) + 8
			count++
		}
	}
	for a := byte('!'); a <= '~'; a++ {
		for b := byte('!'); b <= '~'; b++ {
			for c := byte('!'); c <= '~'; c++ {
				name := string([]byte{a, b, c})
				if name == "~~~" {
					continue
				}
				if used+len(name)+8 > budget {
					if count < 23000 {
						t.Fatalf("dense fixture has only%d tuples", count)
					}
					return
				}
				cert.Extensions[name] = ""
				used += len(name) + 8
				count++
			}
		}
	}
	t.Fatal("dense metadata did not reach its maximum short-name shape")
}

func agentMemoryNestedCertificate(t *testing.T, outer *ssh.Certificate, padding int) []byte {
	t.Helper()
	base := *outer
	base.Extensions = nil
	base.Signature = outer.Signature
	ca := base.SignatureKey.Marshal()
	// The outer certificate's CA is another certificate, never an allowed
	// plain CA key. A speculative recursive SDK parse sees the whole bomb.
	for range 4096 {
		base.SignatureKey = &agentMemoryRawPublicKey{blob: ca, kind: outer.Type()}
		next := base.Marshal()
		if len(next) > agentFWMaxBody-1024 {
			break
		}
		ca = next
	}
	copy := *outer
	copy.Extensions = map[string]string{"~~~": strings.Repeat("x", padding)}
	copy.SignatureKey = &agentMemoryRawPublicKey{blob: ca, kind: outer.Type()}
	return copy.Marshal()
}

type agentMemoryRawPublicKey struct {
	blob []byte
	kind string
}

func (k *agentMemoryRawPublicKey) Type() string                      { return k.kind }
func (k *agentMemoryRawPublicKey) Marshal() []byte                   { return k.blob }
func (*agentMemoryRawPublicKey) Verify([]byte, *ssh.Signature) error { return agentBindingProofError() }
