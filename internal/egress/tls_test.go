package egress

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

func captureClientHello(t testing.TB, cfg *tls.Config) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		tc := tls.Client(c1, cfg)
		_ = tc.Handshake()
		_ = tc.Close()
	}()

	ch, err := ReadClientHello(c2)
	if err != nil {
		t.Fatalf("ReadClientHello failed on standard tls.Client output: %v", err)
	}
	return ch.Raw
}

// appendExtensionOnRecord injects an extra TLS extension (extType, extPayload)
// into a single-record ClientHello for adversarial testing.
func appendExtensionOnRecord(t *testing.T, rawRecord []byte, extType uint16, extPayload []byte) []byte {
	t.Helper()
	if len(rawRecord) < 9 || rawRecord[0] != 0x16 || rawRecord[5] != 0x01 {
		t.Fatalf("unexpected TLS record header: %x", rawRecord[:min(len(rawRecord), 9)])
	}
	ext := make([]byte, 4+len(extPayload))
	binary.BigEndian.PutUint16(ext[0:2], extType)
	binary.BigEndian.PutUint16(ext[2:4], uint16(len(extPayload)))
	copy(ext[4:], extPayload)

	out := append([]byte(nil), rawRecord...)
	// Parse handshake body inside out[9:] to find the extensions length field.
	body := out[9:]
	pos := 34 // client_version(2) + random(32)
	sidLen := int(body[pos])
	pos += 1 + sidLen
	csLen := int(binary.BigEndian.Uint16(body[pos : pos+2]))
	pos += 2 + csLen
	compLen := int(body[pos])
	pos += 1 + compLen
	extTotal := int(binary.BigEndian.Uint16(body[pos : pos+2]))
	binary.BigEndian.PutUint16(body[pos:pos+2], uint16(extTotal+len(ext)))

	out = append(out, ext...)
	recLen := len(out) - 5
	binary.BigEndian.PutUint16(out[3:5], uint16(recLen))
	hsLen := len(out) - 9
	out[6] = byte(hsLen >> 16)
	out[7] = byte(hsLen >> 8)
	out[8] = byte(hsLen)
	return out
}

// fragmentRecord splits a single TLS handshake record into tiny 3-byte records
// across the header and session ID, then chunkSize-byte records thereafter.
func fragmentRecord(rawRecord []byte, chunkSize int) []byte {
	payload := rawRecord[5:]
	var out bytes.Buffer
	for recs := 0; len(payload) > 0; recs++ {
		step := chunkSize
		if recs < 10 {
			step = 3
		}
		n := min(step, len(payload))
		out.WriteByte(rawRecord[0])
		out.WriteByte(rawRecord[1])
		out.WriteByte(rawRecord[2])
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(n))
		out.Write(lenBuf[:])
		out.Write(payload[:n])
		payload = payload[n:]
	}
	return out.Bytes()
}

func TestReadAndCheckClientHello(t *testing.T) {
	raw := captureClientHello(t, &tls.Config{ServerName: "registry.npmjs.org", InsecureSkipVerify: true})
	ch, err := ReadClientHello(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ch.ServerName != "registry.npmjs.org" || ch.HasECH {
		t.Fatalf("parsed ClientHello = %+v", ch)
	}
	if !bytes.Equal(ch.Raw, raw) {
		t.Fatal("ReadClientHello did not preserve exact raw record bytes")
	}
	if err := CheckClientHello(ch, "registry.npmjs.org"); err != nil {
		t.Fatalf("CheckClientHello matching SNI failed: %v", err)
	}
	if err := CheckClientHello(ch, "example.com"); err == nil {
		t.Fatal("CheckClientHello accepted mismatched SNI")
	}

	// Fragmented across tiny 3-byte and 64-byte TLS records still parses and preserves bytes.
	frag := fragmentRecord(raw, 64)
	chFrag, err := ReadClientHello(bytes.NewReader(frag))
	if err != nil {
		t.Fatalf("fragmented ReadClientHello failed: %v", err)
	}
	if err := CheckClientHello(chFrag, "registry.npmjs.org"); err != nil {
		t.Fatalf("fragmented CheckClientHello failed: %v", err)
	}
	if !bytes.Equal(chFrag.Raw, frag) {
		t.Fatal("fragmented Raw bytes mismatch")
	}
}

func TestClientHelloMissingSNIAndNumericIP(t *testing.T) {
	rawNoSNI := captureClientHello(t, &tls.Config{InsecureSkipVerify: true})
	chNoSNI, err := ReadClientHello(bytes.NewReader(rawNoSNI))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckClientHello(chNoSNI, "registry.npmjs.org"); err == nil {
		t.Fatal("hostname target accepted ClientHello without SNI")
	}
	if err := CheckClientHello(chNoSNI, "203.0.113.10"); err != nil {
		t.Fatalf("numeric IP target rejected ClientHello without SNI: %v", err)
	}

	rawWithSNI := captureClientHello(t, &tls.Config{ServerName: "registry.npmjs.org", InsecureSkipVerify: true})
	chWithSNI, err := ReadClientHello(bytes.NewReader(rawWithSNI))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckClientHello(chWithSNI, "203.0.113.10"); err == nil {
		t.Fatal("numeric IP target accepted ClientHello carrying a hostname SNI")
	}
}

func TestClientHelloRefusesECHAndDuplicateExtensions(t *testing.T) {
	base := captureClientHello(t, &tls.Config{ServerName: "registry.npmjs.org", InsecureSkipVerify: true})

	for _, extType := range []uint16{0xfe0d, 0xfd00, 0xfe0a, 0xffce} {
		echRaw := appendExtensionOnRecord(t, base, extType, []byte{0x01, 0x00, 0x20, 0xaa, 0xbb})
		ch, err := ReadClientHello(bytes.NewReader(echRaw))
		if err != nil {
			t.Fatalf("ReadClientHello(ext %#x) err = %v", extType, err)
		}
		if !ch.HasECH {
			t.Fatalf("ext %#x did not set HasECH", extType)
		}
		if err := CheckClientHello(ch, "registry.npmjs.org"); !errors.Is(err, ErrECH) {
			t.Fatalf("CheckClientHello(ext %#x) err = %v, want ErrECH", extType, err)
		}
	}

	// Duplicate server_name extension (0x0000) is rejected in ReadClientHello.
	dupSNI := appendExtensionOnRecord(t, base, 0x0000, []byte{0x00, 0x0e, 0x00, 0x00, 0x0b, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm'})
	if _, err := ReadClientHello(bytes.NewReader(dupSNI)); err == nil {
		t.Fatal("duplicate server_name extension was accepted")
	}

	// Truncated TLS record fails cleanly with an error.
	if _, err := ReadClientHello(io.LimitReader(bytes.NewReader(base), 12)); err == nil {
		t.Fatal("truncated TLS record was accepted")
	}
}
