package egress

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func physicalReaderResetPair(t *testing.T, certificate *tls.Certificate, roots *x509.CertPool) (*physicalH2Conn, *tls.Conn) {
	t.Helper()
	guest, native := net.Pipe()
	_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
	_ = native.SetDeadline(time.Now().Add(5 * time.Second))
	server := tls.Server(native, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{*certificate}})
	client := tls.Client(guest, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, ServerName: "reader.example.test", RootCAs: roots})
	handshake := make(chan error, 1)
	go func() { handshake <- server.HandshakeContext(t.Context()) }()
	if err := client.HandshakeContext(t.Context()); err != nil || <-handshake != nil {
		t.Fatal("actual original TLS handshake failed", err)
	}
	conn, err := newPhysicalH2Conn(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}
	conn.owner.scheduler = true
	t.Cleanup(func() { _ = client.NetConn().Close(); conn.joinPhysicalH2() })
	return conn, client
}

func TestPhysicalH2PolicyWakeWaitsForOriginalContinuationAndKeepsDictionary(t *testing.T) {
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("reader.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	conn, client := physicalReaderResetPair(t, certificate, roots)
	type fragment struct {
		id      uint32
		kind    http2.FrameType
		end     bool
		payload []byte
		err     error
	}
	frames := make(chan fragment, 8)
	prefaced := make(chan struct{})
	go func() {
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(conn, preface); err != nil {
			frames <- fragment{err: err}
			return
		}
		close(prefaced)
		reader := http2.NewFramer(io.Discard, conn)
		for index := 0; index < 6; index++ {
			frame, err := reader.ReadFrame()
			if err != nil {
				frames <- fragment{err: err}
				return
			}
			value := fragment{id: frame.Header().StreamID, kind: frame.Header().Type}
			switch frame := frame.(type) {
			case *http2.HeadersFrame:
				value.end, value.payload = frame.HeadersEnded(), bytes.Clone(frame.HeaderBlockFragment())
			case *http2.ContinuationFrame:
				value.end, value.payload = frame.HeadersEnded(), bytes.Clone(frame.HeaderBlockFragment())
			}
			frames <- value
		}
	}()
	if _, err := io.WriteString(client, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, prefaced, "the original continuation fixture preface")
	stream, _, err := conn.owner.introduce(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "reader.example.test"}, {Name: "x-original", Value: "dictionary-value"}}
	encoded := physicalH2Fields(t, encoder, &block, fields)
	middle := len(encoded) / 2
	writer := http2.NewFramer(client, nil)
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 3, BlockFragment: encoded[:middle], EndHeaders: false, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	var first fragment
	select {
	case first = <-frames:
	case <-time.After(5 * time.Second):
		t.Fatal("original HEADERS fragment never crossed")
	}
	if first.err != nil || first.kind != http2.FrameHeaders || first.id != 3 || first.end || !bytes.Equal(first.payload, encoded[:middle]) {
		t.Fatal("original first fragment changed", first.err)
	}
	stream.invalidate()
	if err := writer.WriteContinuation(3, true, encoded[middle:]); err != nil {
		t.Fatal(err)
	}
	second := physicalH2Fields(t, encoder, &block, fields)
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 5, BlockFragment: second, EndHeaders: true, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	all := []fragment{first}
	for len(all) < 6 {
		select {
		case frame := <-frames:
			if frame.err != nil {
				t.Fatal("private reset interrupted a valid header block", frame.err)
			}
			all = append(all, frame)
		case <-time.After(5 * time.Second):
			t.Fatal("original continuation, reset or next indexed request stalled")
		}
	}
	if all[1].kind != http2.FrameContinuation || all[1].id != 3 || all[1].end || !bytes.Equal(all[1].payload, encoded[middle:]) || all[2].kind != http2.FrameContinuation || !all[2].end || all[3].kind != http2.FrameRSTStream || all[3].id != 1 || all[4].kind != http2.FrameHeaders || all[4].id != 5 || all[5].kind != http2.FrameContinuation || !all[5].end {
		t.Fatal("private reset crossed the original CONTINUATION boundary")
	}
	var decoded []hpack.HeaderField
	decoder := hpack.NewDecoder(physicalH2OriginalTableLimit, func(field hpack.HeaderField) {
		if field.Name != physicalH2TokenHeader {
			decoded = append(decoded, field)
		}
	})
	for _, index := range []int{0, 1, 2, 4, 5} {
		if _, err := decoder.Write(all[index].payload); err != nil {
			t.Fatal(err)
		}
		if all[index].end {
			if err := decoder.Close(); err != nil {
				t.Fatal(err)
			}
			if len(decoded) != len(fields) {
				t.Fatal("original dictionary lost a header")
			}
			for position, field := range decoded {
				if field.Name != fields[position].Name || field.Value != fields[position].Value {
					t.Fatal("private reset changed the original HPACK dictionary")
				}
			}
			decoded = nil
		}
	}
}

func TestPhysicalH2PolicyReaderWakePreservesEveryNativeFrameCut(t *testing.T) {
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("reader.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	var encoded bytes.Buffer
	if err := http2.NewFramer(&encoded, nil).WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 12345}); err != nil {
		t.Fatal(err)
	}
	wire := bytes.Clone(encoded.Bytes())
	for cut := 0; cut <= len(wire); cut++ {
		conn, client := physicalReaderResetPair(t, certificate, roots)
		prefaced := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			preface := make([]byte, len(http2.ClientPreface))
			if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != http2.ClientPreface {
				result <- errPhysicalH2Binding
				return
			}
			close(prefaced)
			reader := http2.NewFramer(io.Discard, conn)
			settings, reset := false, false
			for index := 0; index < 2; index++ {
				frame, err := reader.ReadFrame()
				if err != nil {
					result <- err
					return
				}
				switch frame := frame.(type) {
				case *http2.SettingsFrame:
					value, ok := frame.Value(http2.SettingInitialWindowSize)
					if !ok || value != 12345 {
						result <- errPhysicalH2Binding
						return
					}
					settings = true
				case *http2.RSTStreamFrame:
					if frame.StreamID != 1 || frame.ErrCode != http2.ErrCodeCancel {
						result <- errPhysicalH2Binding
						return
					}
					reset = true
				default:
					result <- errPhysicalH2Binding
					return
				}
			}
			if !settings || !reset {
				result <- errPhysicalH2Binding
				return
			}
			result <- nil
		}()
		if _, err := io.WriteString(client, http2.ClientPreface); err != nil {
			t.Fatal(err)
		}
		waitHTTPProgress(t, prefaced, "the original native HTTP2 preface")
		stream, _, err := conn.owner.introduce(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if cut > 0 {
			if count, err := client.Write(wire[:cut]); err != nil || count != cut {
				t.Fatal("selected original frame prefix failed", err)
			}
		}
		stream.invalidate()
		if cut < len(wire) {
			if count, err := client.Write(wire[cut:]); err != nil || count != len(wire)-cut {
				t.Fatal("resumed original TLS record failed", err)
			}
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("cut%d lost a native prefix or corrupted the original frame after read wake: %v", cut, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("cut%d retained a blocked original reader", cut)
		}
		_ = client.NetConn().Close()
		conn.joinPhysicalH2()
	}
}
