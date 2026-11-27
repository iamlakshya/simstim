package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type protocolOracleWriteGate struct {
	net.Conn
	mu          sync.Mutex
	armed       bool
	started     chan struct{}
	release     chan struct{}
	closed      chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	closeOnce   sync.Once
}

func (g *protocolOracleWriteGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	armed := g.armed
	g.mu.Unlock()
	if armed {
		g.startOnce.Do(func() { close(g.started) })
		select {
		case <-g.release:
		case <-g.closed:
			return 0, net.ErrClosed
		}
	}
	return g.Conn.Write(p)
}

func (g *protocolOracleWriteGate) Close() error {
	g.closeOnce.Do(func() { close(g.closed) })
	return g.Conn.Close()
}

func (g *protocolOracleWriteGate) resume() { g.releaseOnce.Do(func() { close(g.release) }) }

func openProtocolOracleGatedTLS(t *testing.T, h *httpProtocolOracle) (*tls.Conn, *protocolOracleWriteGate) {
	t.Helper()
	guest, proxy := net.Pipe()
	gate := &protocolOracleWriteGate{Conn: proxy, started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	t.Cleanup(func() { gate.resume(); _ = guest.Close(); _ = gate.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	go h.env.srv.ServeFlow(ctx, gate, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 443})
	_ = guest.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(guest, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(guest)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("gated CONNECT = %d", response.StatusCode)
	}
	conn := tls.Client(&protocolOracleReaderConn{Conn: guest, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: h.guestRoots, ServerName: "api.example.com", NextProtos: []string{"http/1.1"}})
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	return conn, gate
}

// A frame fixture, not a WebSocket implementation. The proxy must copy these
// independently chosen valid wire bytes without interpreting or remasking them.
func protocolOracleFrame(opcode byte, final, masked bool, payload []byte) []byte {
	if len(payload) > 65535 {
		panic("oracle frame fixture exceeds its two-byte length")
	}
	first := opcode
	if final {
		first |= 0x80
	}
	wire := []byte{first}
	flag := byte(0)
	if masked {
		flag = 0x80
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
	if len(payload) < 126 {
		wire = append(wire, flag|length[7])
	} else {
		wire = append(wire, flag|126, length[6], length[7])
	}
	mask := [4]byte{0x13, 0x37, 0x42, 0x99}
	if masked {
		wire = append(wire, mask[:]...)
	}
	for i, b := range payload {
		if masked {
			b ^= mask[i%len(mask)]
		}
		wire = append(wire, b)
	}
	return wire
}

func TestHTTPProtocolClassicWebSocketKeepsFramesAndBackpressureUntilBothSidesClose(t *testing.T) {
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	// The public RFC 6455 example pair keeps the expected handshake independent
	// of any hashing or handshake implementation in the code under test.
	const accept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	guestWire := append(protocolOracleFrame(2, false, true, []byte{0, 0xff, 0xfe, 7}), protocolOracleFrame(0, true, true, []byte("guest continuation"))...)
	originWire := append(protocolOracleFrame(2, true, false, bytes.Repeat([]byte{0, 0x80, 0xff, 0x1b}, 1024)), protocolOracleFrame(9, true, false, []byte("ping"))...)
	sendOrigin := make(chan struct{})
	var sendOnce sync.Once
	releaseOrigin := func() { sendOnce.Do(func() { close(sendOrigin) }) }
	originRead := make(chan error, 1)
	originWrite := make(chan error, 1)
	originEOF := make(chan error, 1)
	var hijackMu sync.Mutex
	var hijacked net.Conn
	h := newHTTPProtocolOracle(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The origin offers both protocols. Go's standard transport must choose
		// H1 for classic WebSocket, without disabling H2 for ordinary requests.
		if err := protocolOracleRequestError(r, 1, http.MethodGet, "/allowed/socket"); err != nil {
			originRead <- err
			http.Error(w, "wrong upgrade protocol", 500)
			return
		}
		if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Sec-WebSocket-Key") != key {
			originRead <- errors.New("upgrade identity was changed")
			http.Error(w, "wrong upgrade headers", 500)
			return
		}
		conn, buffer, err := http.NewResponseController(w).Hijack()
		if err != nil {
			originRead <- err
			return
		}
		hijackMu.Lock()
		hijacked = conn
		hijackMu.Unlock()
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		if _, err := fmt.Fprintf(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
			originRead <- err
			return
		}
		if err := buffer.Flush(); err != nil {
			originRead <- err
			return
		}
		select {
		case <-sendOrigin:
		case <-r.Context().Done():
			originRead <- r.Context().Err()
			return
		}
		go func() { _, err := conn.Write(originWire); originWrite <- err }()
		got := make([]byte, len(guestWire))
		_, err = io.ReadFull(buffer, got)
		if err == nil && !bytes.Equal(got, guestWire) {
			err = fmt.Errorf("guest WebSocket wire bytes changed: %x", got)
		}
		originRead <- err
		var probe [1]byte
		_, err = conn.Read(probe[:])
		originEOF <- err
	}))
	t.Cleanup(func() {
		releaseOrigin()
		hijackMu.Lock()
		if hijacked != nil {
			_ = hijacked.Close()
		}
		hijackMu.Unlock()
	})
	guest, gate := openProtocolOracleGatedTLS(t, h)
	defer func() { gate.resume(); _ = guest.Close() }()
	if _, err := fmt.Fprintf(guest, "GET /allowed/%%73ocket?kept=yes HTTP/1.1\r\nHost: api.example.com:443\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", key); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(guest)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Upgrade") != "websocket" || response.Header.Get("Sec-WebSocket-Accept") != accept {
		t.Fatalf("upgrade response = %d %v", response.StatusCode, response.Header)
	}
	gate.mu.Lock()
	gate.armed = true
	gate.mu.Unlock()
	releaseOrigin()
	select {
	case <-gate.started:
	case <-gate.closed:
		t.Fatal("upgrade closed before its first frame")
	case <-time.After(5 * time.Second):
		t.Fatal("origin frame never reached guest backpressure")
	}
	guestWritten := make(chan error, 1)
	go func() { _, err := guest.Write(guestWire); guestWritten <- err }()
	select {
	case err := <-originRead:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guest frames stopped behind reverse-direction backpressure")
	}
	select {
	case <-gate.closed:
		t.Fatal("hijacked stream closed while its response bytes were blocked")
	default:
	}
	gate.resume()
	got := make([]byte, len(originWire))
	if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, originWire) {
		t.Fatalf("origin WebSocket frame bytes changed: bytes=%d error=%v", len(got), err)
	}
	if err := <-guestWritten; err != nil {
		t.Fatal(err)
	}
	if err := <-originWrite; err != nil {
		t.Fatal(err)
	}
	_ = guest.Close()
	select {
	case err := <-originEOF:
		var timeout net.Error
		if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatalf("guest close did not end the origin stream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guest close left the upgraded origin connection alive")
	}
	if h.dials.Load() != 1 || h.lookups.Load() != 1 {
		t.Fatalf("upgrade dials=%d lookups=%d", h.dials.Load(), h.lookups.Load())
	}
	requireProtocolOracleAudit(t, h, http.MethodGet, "/allowed/socket", http.StatusSwitchingProtocols)
}
