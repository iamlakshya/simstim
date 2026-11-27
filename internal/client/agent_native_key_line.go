package client

import (
	"bytes"
	"strconv"
)

// nativeAgentKeyLine preserves the native plain/certificate label. Its
// untrusted ID/CA display text never becomes key or certificate authority.
func nativeAgentKeyLine(line []byte) (kind, fingerprint []byte, certificate, matched bool, err error) {
	const plainPrefix = "debug1: Server host key:"
	const certificatePrefix = "debug1: Server host certificate:"
	var prefix string
	switch {
	case bytes.HasPrefix(line, []byte(plainPrefix)):
		prefix = plainPrefix
	case bytes.HasPrefix(line, []byte(certificatePrefix)):
		prefix, certificate = certificatePrefix, true
	default:
		return nil, nil, false, false, nil
	}
	matched = true
	bad := func() ([]byte, []byte, bool, bool, error) {
		return nil, nil, false, true, agentBindingProofError()
	}
	rest, ok := bytes.CutPrefix(line[len(prefix):], []byte(" "))
	if !ok {
		return bad()
	}
	kind, rest, ok = bytes.Cut(rest, []byte(" "))
	if !ok || len(rest) < 50 || !fingerprintRE.Match(rest[:50]) {
		return bad()
	}
	fingerprint, suffix := rest[:50], rest[50:]
	_, fields := agentCertificateForm(string(kind))
	if certificate {
		if fields == 0 || !nativeAgentCertificatePrefix(suffix) {
			return bad()
		}
	} else if fields != 0 || !agentPlainKeyType(string(kind)) || len(suffix) != 0 {
		return bad()
	}
	return kind, fingerprint, certificate, matched, nil
}

// The native caller's fixed fields precede its untrusted display ID. That
// tail may be internally truncated or contain a fake closing/CA suffix.
// Only the complete native-first raw binding supplies the certificate.
func nativeAgentCertificatePrefix(suffix []byte) bool {
	rest, ok := bytes.CutPrefix(suffix, []byte(", serial "))
	if !ok {
		return false
	}
	serial, _, ok := bytes.Cut(rest, []byte(` ID "`))
	if !ok || len(serial) == 0 || len(serial) > 20 {
		return false
	}
	for _, digit := range serial {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(string(serial), 10, 64)
	return err == nil
}
