package egress

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	tlsRecordHandshake  = 0x16
	tlsHandshakeHello   = 0x01
	maxTLSRecordPayload = 16384
	maxClientHelloBytes = 64 << 10
	maxClientHelloRecs  = 64

	extServerName = 0x0000
	// ECH (RFC 9849), outer-extensions and earlier ECH/ESNI draft codepoints.
	extECH          = 0xfe0d
	extECHOuterExts = 0xfd00
	extECHDraft08   = 0xfe08
	extECHDraft09   = 0xfe09
	extECHDraft10   = 0xfe0a
	extECHDraft11   = 0xfe0b
	extECHDraft12   = 0xfe0c
	extLegacyESNI   = 0xffce
)

// ErrECH marks a TLS ClientHello that carries Encrypted Client Hello or ESNI.
var ErrECH = errors.New("TLS ClientHello carries encrypted_client_hello")

// ErrSNI marks a TLS ClientHello whose SNI is missing, invalid or mismatched.
var ErrSNI = errors.New("TLS ClientHello SNI does not match destination host")

// ClientHello holds the parsed fields of a TLS ClientHello and the raw record
// bytes read from the client, ready to forward upstream when allowed.
type ClientHello struct {
	ServerName    string
	RawServerName string
	HasECH        bool
	Raw           []byte
}

// ReadClientHello reads TLS Handshake records from r up to a complete
// ClientHello (at most 64 KiB across 64 records) and parses its extensions.
func ReadClientHello(r io.Reader) (ClientHello, error) {
	var raw []byte
	var hs []byte
	need := 4

	for recs := 0; recs < maxClientHelloRecs && len(hs) < need; recs++ {
		var hdr [5]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return ClientHello{}, fmt.Errorf("read TLS record header: %w", err)
		}
		if hdr[0] != tlsRecordHandshake {
			return ClientHello{}, fmt.Errorf("%w: unexpected TLS record type 0x%02x", ErrSNI, hdr[0])
		}
		ver := binary.BigEndian.Uint16(hdr[1:3])
		if ver < 0x0300 || ver > 0x0304 {
			return ClientHello{}, fmt.Errorf("%w: unsupported TLS record version 0x%04x", ErrSNI, ver)
		}
		recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
		if recLen == 0 || recLen > maxTLSRecordPayload {
			return ClientHello{}, fmt.Errorf("%w: invalid TLS record length %d", ErrSNI, recLen)
		}
		if len(hs)+recLen > maxClientHelloBytes+4 {
			return ClientHello{}, fmt.Errorf("%w: TLS ClientHello exceeds %d bytes", ErrSNI, maxClientHelloBytes)
		}
		payload := make([]byte, recLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			return ClientHello{}, fmt.Errorf("read TLS record body: %w", err)
		}
		raw = append(raw, hdr[:]...)
		raw = append(raw, payload...)
		hs = append(hs, payload...)

		if len(hs) >= 4 {
			if hs[0] != tlsHandshakeHello {
				return ClientHello{}, fmt.Errorf("%w: first TLS handshake message is 0x%02x, want ClientHello", ErrSNI, hs[0])
			}
			msgLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
			if msgLen == 0 || msgLen > maxClientHelloBytes {
				return ClientHello{}, fmt.Errorf("%w: invalid ClientHello length %d", ErrSNI, msgLen)
			}
			need = 4 + msgLen
		}
	}
	if len(hs) != need {
		return ClientHello{}, fmt.Errorf("%w: incomplete or trailing handshake data in ClientHello flight", ErrSNI)
	}
	var originalSNI string
	sni, hasECH, err := parseClientHelloBodyOriginal(hs[4:need], &originalSNI)
	if err != nil {
		return ClientHello{}, err
	}
	return ClientHello{ServerName: sni, RawServerName: originalSNI, HasECH: hasECH, Raw: raw}, nil
}

// CheckClientHello verifies ch against expectedHost: it refuses ECH and checks
// that SNI matches expectedHost (or is absent when expectedHost is an IP).
func CheckClientHello(ch ClientHello, expectedHost string) error {
	if ch.HasECH {
		return ErrECH
	}
	want, err := NormaliseHost(expectedHost)
	if err != nil {
		return fmt.Errorf("%w: invalid expected host", ErrSNI)
	}
	if isNumericAddr(want) {
		if ch.ServerName != "" {
			return fmt.Errorf("%w: unexpected SNI %q for IP destination %s", ErrSNI, ch.ServerName, want)
		}
		return nil
	}
	if ch.ServerName == "" {
		return fmt.Errorf("%w: missing server_name extension for %s", ErrSNI, want)
	}
	if ch.ServerName != want {
		return fmt.Errorf("%w: SNI %q != %q", ErrSNI, ch.ServerName, want)
	}
	return nil
}

func parseClientHelloBody(b []byte) (sni string, hasECH bool, err error) {
	return parseClientHelloBodyOriginal(b, nil)
}

func parseClientHelloBodyOriginal(b []byte, original *string) (sni string, hasECH bool, err error) {
	// legacy_version (2) + random (32) + session_id len (1)
	if len(b) < 35 {
		return "", false, fmt.Errorf("%w: truncated ClientHello header", ErrSNI)
	}
	ver := binary.BigEndian.Uint16(b[0:2])
	if ver < 0x0300 || ver > 0x0304 {
		return "", false, fmt.Errorf("%w: unsupported ClientHello version 0x%04x", ErrSNI, ver)
	}
	rest := b[34:]
	sidLen := int(rest[0])
	rest = rest[1:]
	if sidLen > 32 || len(rest) < sidLen+2 {
		return "", false, fmt.Errorf("%w: invalid ClientHello session_id", ErrSNI)
	}
	rest = rest[sidLen:]

	csLen := int(binary.BigEndian.Uint16(rest[0:2]))
	rest = rest[2:]
	if csLen < 2 || csLen%2 != 0 || len(rest) < csLen+1 {
		return "", false, fmt.Errorf("%w: invalid ClientHello cipher_suites", ErrSNI)
	}
	rest = rest[csLen:]

	compLen := int(rest[0])
	rest = rest[1:]
	if compLen < 1 || len(rest) < compLen {
		return "", false, fmt.Errorf("%w: invalid ClientHello compression_methods", ErrSNI)
	}
	rest = rest[compLen:]
	if len(rest) == 0 {
		return "", false, nil
	}
	if len(rest) < 2 {
		return "", false, fmt.Errorf("%w: truncated ClientHello extensions length", ErrSNI)
	}
	extTotal := int(binary.BigEndian.Uint16(rest[0:2]))
	rest = rest[2:]
	if extTotal != len(rest) {
		return "", false, fmt.Errorf("%w: ClientHello extensions length mismatch", ErrSNI)
	}
	return parseExtensionsOriginal(rest, original)
}

func parseExtensions(rest []byte) (sni string, hasECH bool, err error) {
	return parseExtensionsOriginal(rest, nil)
}

func parseExtensionsOriginal(rest []byte, original *string) (sni string, hasECH bool, err error) {
	seen := make(map[uint16]bool, 16)
	for len(rest) > 0 {
		if len(rest) < 4 {
			return "", false, fmt.Errorf("%w: truncated TLS extension header", ErrSNI)
		}
		extType := binary.BigEndian.Uint16(rest[0:2])
		extLen := int(binary.BigEndian.Uint16(rest[2:4]))
		rest = rest[4:]
		if len(rest) < extLen {
			return "", false, fmt.Errorf("%w: truncated TLS extension 0x%04x", ErrSNI, extType)
		}
		body := rest[:extLen]
		rest = rest[extLen:]

		if seen[extType] {
			return "", false, fmt.Errorf("%w: duplicate TLS extension 0x%04x", ErrSNI, extType)
		}
		seen[extType] = true

		if isECHExtension(extType) {
			hasECH = true
		}
		if extType == extServerName {
			name, err := parseServerNameExtOriginal(body, original)
			if err != nil {
				return "", hasECH, err
			}
			sni = name
		}
	}
	return sni, hasECH, nil
}

func isECHExtension(extType uint16) bool {
	switch extType {
	case extECH, extECHOuterExts, extECHDraft08, extECHDraft09, extECHDraft10, extECHDraft11, extECHDraft12, extLegacyESNI:
		return true
	default:
		return false
	}
}

func parseServerNameExt(b []byte) (string, error) {
	return parseServerNameExtOriginal(b, nil)
}

func parseServerNameExtOriginal(b []byte, original *string) (string, error) {
	if len(b) < 2 {
		return "", fmt.Errorf("%w: empty server_name extension", ErrSNI)
	}
	listLen := int(binary.BigEndian.Uint16(b[0:2]))
	rest := b[2:]
	if listLen == 0 || listLen != len(rest) {
		return "", fmt.Errorf("%w: invalid server_name list length", ErrSNI)
	}
	var hostName string
	seenHost := false
	for len(rest) > 0 {
		if len(rest) < 3 {
			return "", fmt.Errorf("%w: truncated server_name entry", ErrSNI)
		}
		nameType := rest[0]
		nameLen := int(binary.BigEndian.Uint16(rest[1:3]))
		rest = rest[3:]
		if nameLen == 0 || len(rest) < nameLen {
			return "", fmt.Errorf("%w: invalid server_name entry length", ErrSNI)
		}
		rawName := string(rest[:nameLen])
		rest = rest[nameLen:]
		if nameType != 0 {
			continue
		}
		if seenHost {
			return "", fmt.Errorf("%w: duplicate host_name in server_name extension", ErrSNI)
		}
		seenHost = true
		norm, err := NormaliseHost(rawName)
		if err != nil || isNumericAddr(norm) {
			return "", fmt.Errorf("%w: invalid SNI host %q", ErrSNI, rawName)
		}
		if original != nil {
			*original = rawName
		}
		hostName = norm
	}
	if !seenHost {
		return "", fmt.Errorf("%w: server_name extension has no host_name", ErrSNI)
	}
	return hostName, nil
}
