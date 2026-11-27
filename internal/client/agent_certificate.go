package client

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/crypto/ssh"
)

// parseAgentBindingKey retains the original certificate as the binding's
// identity. This view parses only its leaf with the pinned SSH library and
// walks metadata without materialising principal strings or option entries. Native
// provenance is still proved separately by the owned first-hook collector.
func parseAgentBindingKey(blob []byte) (ssh.PublicKey, string, error) {
	algorithm, rest, ok := takeAgentFirewallString(blob)
	if !ok {
		return nil, "", agentBindingProofError()
	}
	_, fields := agentCertificateForm(string(algorithm))
	if fields == 0 {
		if !agentPlainKeyType(string(algorithm)) {
			return nil, "", agentBindingProofError()
		}
		key, err := ssh.ParsePublicKey(blob)
		if err != nil {
			return nil, "", agentBindingProofError()
		}
		return key, key.Type(), nil
	}
	_, rest, ok = takeAgentFirewallString(rest) // Nonce.
	if !ok {
		return nil, "", agentBindingProofError()
	}
	leaf := rest
	for range fields {
		_, rest, ok = takeAgentFirewallString(rest)
		if !ok {
			return nil, "", agentBindingProofError()
		}
	}
	leaf = leaf[:len(leaf)-len(rest)]
	parts, ok := projectAgentCertificateTail(rest)
	if !ok {
		return nil, "", agentBindingProofError()
	}
	// Exactly one bounded certificate parse delegates leaf, CA key and
	// CA signature grammar to the pinned library. Only irrelevant metadata
	// is empty in this temporary view. The original wire never changes.
	view := make([]byte, 0, 64+len(algorithm)+len(leaf)+len(parts.ca)+len(parts.signature))
	view = appendAgentCertificateString(view, algorithm)
	view = appendAgentCertificateString(view, nil) // Nonce.
	view = append(view, leaf...)
	view = append(view, parts.serialType...)
	view = appendAgentCertificateString(view, nil) // Key ID.
	view = appendAgentCertificateString(view, nil) // Principals.
	view = append(view, parts.validity...)
	view = appendAgentCertificateString(view, nil) // Critical options.
	view = appendAgentCertificateString(view, nil) // Extensions.
	view = appendAgentCertificateString(view, nil) // Reserved.
	view = appendAgentCertificateString(view, parts.ca)
	view = appendAgentCertificateString(view, parts.signature)
	parsed, err := ssh.ParsePublicKey(view)
	if err != nil {
		return nil, "", agentBindingProofError()
	}
	certificate, ok := parsed.(*ssh.Certificate)
	if !ok {
		return nil, "", agentBindingProofError()
	}
	return certificate.Key, certificate.Type(), nil
}

func appendAgentCertificateString(dst, value []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func agentPlainKeyType(algorithm string) bool {
	switch algorithm {
	case ssh.KeyAlgoRSA, ssh.InsecureKeyAlgoDSA, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoSKECDSA256, ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519, ssh.KeyAlgoMLDSA44, ssh.KeyAlgoMLDSA65, ssh.KeyAlgoMLDSA87:
		return true
	default:
		return false
	}
}

// The wire classes and component counts match x/crypto v0.58.0 parsePubKey.
// Signature-only RSA-SHA2 certificate algorithm names are deliberately not
// wire classes. ML-DSA availability remains the library's build-tag decision.
func agentCertificateForm(algorithm string) (plain string, fields int) {
	switch algorithm {
	case ssh.CertAlgoRSAv01:
		return ssh.KeyAlgoRSA, 2
	case ssh.InsecureCertAlgoDSAv01:
		return ssh.InsecureKeyAlgoDSA, 4
	case ssh.CertAlgoECDSA256v01:
		return ssh.KeyAlgoECDSA256, 2
	case ssh.CertAlgoECDSA384v01:
		return ssh.KeyAlgoECDSA384, 2
	case ssh.CertAlgoECDSA521v01:
		return ssh.KeyAlgoECDSA521, 2
	case ssh.CertAlgoSKECDSA256v01:
		return ssh.KeyAlgoSKECDSA256, 3
	case ssh.CertAlgoED25519v01:
		return ssh.KeyAlgoED25519, 1
	case ssh.CertAlgoSKED25519v01:
		return ssh.KeyAlgoSKED25519, 2
	case ssh.CertAlgoMLDSA44v01Go:
		return ssh.KeyAlgoMLDSA44, 1
	case ssh.CertAlgoMLDSA65v01Go:
		return ssh.KeyAlgoMLDSA65, 1
	case ssh.CertAlgoMLDSA87v01Go:
		return ssh.KeyAlgoMLDSA87, 1
	default:
		return "", 0
	}
}

func agentCertificateName(algorithm []byte) bool {
	if _, fields := agentCertificateForm(string(algorithm)); fields != 0 {
		return true
	}
	// The full parser rejects these CA names before attempting recursion,
	// although they are not accepted public-key wire encodings themselves.
	return bytes.Equal(algorithm, []byte(ssh.CertAlgoRSASHA256v01)) ||
		bytes.Equal(algorithm, []byte(ssh.CertAlgoRSASHA512v01))
}

type agentCertCursor struct{ rest []byte }

func (c *agentCertCursor) fixed(size int) bool {
	if len(c.rest) < size {
		return false
	}
	c.rest = c.rest[size:]
	return true
}

func (c *agentCertCursor) string() ([]byte, bool) {
	value, rest, ok := takeAgentFirewallString(c.rest)
	if ok {
		c.rest = rest
	}
	return value, ok
}

type agentCertificateParts struct {
	serialType, validity, ca, signature []byte
}

func projectAgentCertificateTail(tail []byte) (agentCertificateParts, bool) {
	var parts agentCertificateParts
	c := agentCertCursor{rest: tail}
	if len(c.rest) < 12 {
		return parts, false
	}
	parts.serialType = c.rest[:12] // Any uint32 type accepted by the parser.
	c.fixed(12)
	if _, ok := c.string(); !ok { // Key ID.
		return parts, false
	}
	principals, ok := c.string()
	if !ok || !validAgentCertificatePrincipals(principals) || len(c.rest) < 16 {
		return parts, false
	}
	parts.validity = c.rest[:16]
	c.fixed(16)
	critical, cok := c.string()
	extensions, eok := c.string()
	if !cok || !eok || !validAgentCertificateTuples(critical) || !validAgentCertificateTuples(extensions) {
		return parts, false
	}
	if _, ok := c.string(); !ok { // Reserved accepts any bytes.
		return parts, false
	}
	parts.ca, ok = c.string()
	if !ok {
		return parts, false
	}
	caAlgorithm, _, ok := takeAgentFirewallString(parts.ca)
	if !ok || agentCertificateName(caAlgorithm) || !agentPlainKeyType(string(caAlgorithm)) {
		return parts, false
	}
	parts.signature, ok = c.string()
	return parts, ok && len(c.rest) == 0
}

func validAgentCertificatePrincipals(raw []byte) bool {
	for len(raw) != 0 {
		_, rest, ok := takeAgentFirewallString(raw)
		if !ok {
			return false
		}
		raw = rest
	}
	return true
}

func validAgentCertificateTuples(raw []byte) bool {
	var previous []byte
	hadPrevious := false
	for len(raw) != 0 {
		name, rest, ok := takeAgentFirewallString(raw)
		if !ok || hadPrevious && bytes.Compare(name, previous) <= 0 {
			return false
		}
		previous, hadPrevious = name, true
		value, rest, ok := takeAgentFirewallString(rest)
		if !ok {
			return false
		}
		if len(value) != 0 {
			_, extra, ok := takeAgentFirewallString(value)
			if !ok || len(extra) != 0 {
				return false
			}
		}
		raw = rest
	}
	return true
}
