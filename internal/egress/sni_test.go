package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// buildTestClientHello captures a real Go crypto/tls ClientHello for sni and
// optionally appends an extra TLS extension (such as ECH 0xfe0d).
func buildTestClientHello(t *testing.T, sni string, extraExtID uint16, extraExtBody []byte) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close(); _ = c2.Close() }()

	done := make(chan []byte, 1)
	go func() {
		var hdr [5]byte
		if _, err := c2.Read(hdr[:]); err != nil {
			done <- nil
			return
		}
		n := int(binary.BigEndian.Uint16(hdr[3:5]))
		rec := make([]byte, 5+n)
		copy(rec[:5], hdr[:])
		got := 0
		for got < n {
			k, err := c2.Read(rec[5+got:])
			got += k
			if err != nil {
				break
			}
		}
		_ = c2.Close()
		done <- rec[:5+got]
	}()

	cfg := &tls.Config{ServerName: sni, InsecureSkipVerify: true}
	cli := tls.Client(c1, cfg)
	_ = cli.Handshake()
	raw := <-done
	if len(raw) < 10 {
		t.Fatalf("failed to capture ClientHello: %d bytes", len(raw))
	}
	if extraExtID == 0 {
		return raw
	}
	return appendTLSExtension(t, raw, extraExtID, extraExtBody)
}

// appendTLSExtension appends one extension to a single-record ClientHello and
// updates the record length, handshake length and extensions length fields.
func appendTLSExtension(t *testing.T, raw []byte, extID uint16, extBody []byte) []byte {
	t.Helper()
	// Locate the extensions length at the end of the ClientHello by parsing
	// the fixed ClientHello fields: record(5) + handshake(4) + ver(2) + rand(32).
	pos := 5 + 4 + 2 + 32
	if pos >= len(raw) {
		t.Fatal("short ClientHello")
	}
	sessLen := int(raw[pos])
	pos += 1 + sessLen
	csLen := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
	pos += 2 + csLen
	compLen := int(raw[pos])
	pos += 1 + compLen
	extLenPos := pos

	add := make([]byte, 4+len(extBody))
	binary.BigEndian.PutUint16(add[0:2], extID)
	binary.BigEndian.PutUint16(add[2:4], uint16(len(extBody)))
	copy(add[4:], extBody)

	out := make([]byte, len(raw)+len(add))
	copy(out, raw)
	copy(out[len(raw):], add)

	delta := len(add)
	// Update record payload length (bytes 3..5).
	binary.BigEndian.PutUint16(out[3:5], binary.BigEndian.Uint16(out[3:5])+uint16(delta))
	// Update 24-bit handshake message length (bytes 6..9).
	hsLen := int(out[6])<<16 | int(out[7])<<8 | int(out[8])
	hsLen += delta
	out[6], out[7], out[8] = byte(hsLen>>16), byte(hsLen>>8), byte(hsLen)
	// Update 16-bit extensions length.
	binary.BigEndian.PutUint16(out[extLenPos:extLenPos+2], binary.BigEndian.Uint16(out[extLenPos:extLenPos+2])+uint16(delta))
	return out
}

func TestPeekAndCheckClientHello(t *testing.T) {
	t.Run("valid matching SNI", func(t *testing.T) {
		raw := buildTestClientHello(t, "registry.npmjs.org", 0, nil)
		c1, c2 := net.Pipe()
		go func() {
			_, _ = c1.Write(raw)
			_ = c1.Close()
		}()
		ch, replay, err := PeekClientHello(context.Background(), c2, 2*time.Second)
		_ = c2.Close()
		if err != nil {
			t.Fatalf("PeekClientHello: %v", err)
		}
		if ch.ServerName != "registry.npmjs.org" || ch.HasECH {
			t.Fatalf("got %+v, want SNI=registry.npmjs.org HasECH=false", ch)
		}
		if !bytes.Equal(replay, raw) {
			t.Errorf("replay length = %d, want exact %d bytes", len(replay), len(raw))
		}
		if err := CheckClientHello(ch, "registry.npmjs.org"); err != nil {
			t.Errorf("CheckClientHello: %v", err)
		}
		if err := CheckClientHello(ch, "example.com"); err == nil {
			t.Error("CheckClientHello accepted mismatched SNI")
		}
	})

	t.Run("ECH 0xfe0d refused", func(t *testing.T) {
		// Minimal valid-looking outer ECH payload: type=0 (outer), kdf/aead/config/enc/payload.
		raw := buildTestClientHello(t, "cloudflare-ech.com", extEncryptedClientHello, []byte{0, 0, 1, 0, 1, 0, 0, 0, 0, 0})
		c1, c2 := net.Pipe()
		go func() {
			_, _ = c1.Write(raw)
			_ = c1.Close()
		}()
		ch, _, err := PeekClientHello(context.Background(), c2, 2*time.Second)
		_ = c2.Close()
		if err != nil {
			t.Fatalf("PeekClientHello: %v", err)
		}
		if !ch.HasECH {
			t.Fatalf("HasECH = false, want true (extensions=%v)", ch.Extensions)
		}
		if err := CheckClientHello(ch, "cloudflare-ech.com"); err == nil {
			t.Fatal("CheckClientHello accepted ECH ClientHello")
		}
	})

	t.Run("ECH outer extensions 0xfd00 refused", func(t *testing.T) {
		raw := buildTestClientHello(t, "example.com", extECHOuterExtensions, []byte{0, 2, 0, 0})
		c1, c2 := net.Pipe()
		go func() {
			_, _ = c1.Write(raw)
			_ = c1.Close()
		}()
		ch, _, err := PeekClientHello(context.Background(), c2, 2*time.Second)
		_ = c2.Close()
		if err != nil {
			t.Fatalf("PeekClientHello: %v", err)
		}
		if !ch.HasECH {
			t.Fatalf("HasECH = false for 0xfd00")
		}
		if err := CheckClientHello(ch, "example.com"); err == nil {
			t.Fatal("CheckClientHello accepted ECH outer extensions")
		}
	})

	t.Run("IP target requires empty SNI", func(t *testing.T) {
		if err := CheckClientHello(ClientHello{ServerName: ""}, "1.1.1.1"); err != nil {
			t.Errorf("empty SNI on IP target refused: %v", err)
		}
		if err := CheckClientHello(ClientHello{ServerName: "evil.example"}, "1.1.1.1"); err == nil {
			t.Error("non-empty SNI on IP target accepted")
		}
		if err := CheckClientHello(ClientHello{ServerName: ""}, "example.com"); err == nil {
			t.Error("empty SNI on hostname target accepted")
		}
	})

	t.Run("non-TLS garbage refused", func(t *testing.T) {
		c1, c2 := net.Pipe()
		go func() {
			_, _ = c1.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
			_ = c1.Close()
		}()
		if _, _, err := PeekClientHello(context.Background(), c2, 2*time.Second); err == nil {
			t.Fatal("non-TLS payload accepted by PeekClientHello")
		}
		_ = c2.Close()
	})
}

func FuzzPeekClientHello(f *testing.F) {
	f.Add([]byte("GET / HTTP/1.1\r\n\r\n"))
	f.Add([]byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01, 0x00, 0x00, 0x01, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		c1, c2 := net.Pipe()
		go func() {
			_, _ = c1.Write(data)
			_ = c1.Close()
		}()
		ch, replay, err := PeekClientHello(context.Background(), c2, time.Second)
		_ = c2.Close()
		if err == nil {
			if len(replay) > maxClientHelloBytes {
				t.Fatalf("replay exceeded cap: %d", len(replay))
			}
			_ = CheckClientHello(ch, "example.com")
		}
	})
}
