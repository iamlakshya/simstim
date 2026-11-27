package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type physicalWSOriginCut struct {
	io.ReadWriteCloser
	remaining int
}

func (c *physicalWSOriginCut) Write(payload []byte) (int, error) {
	count := min(c.remaining, len(payload))
	if count == 0 {
		return 0, io.ErrClosedPipe
	}
	n, err := c.ReadWriteCloser.Write(payload[:count])
	c.remaining -= n
	if err == nil && count != len(payload) {
		err = io.ErrClosedPipe
	}
	return n, err
}

func TestWebSocketOriginCountsEveryActualTLSFrameCutWithoutMaskOrControls(t *testing.T) {
	const body = `{"type":"response.create","stream_id":"main","model":"native","input":[]}`
	type span struct{ start, end int }
	type frame struct {
		wire         []byte
		payloadStart int
		data         bool
	}
	var frames []frame
	var wire []byte
	var spans []span
	appendFrame := func(opcode byte, final bool, payload string, data bool) {
		encoded := wsTestWire(opcode, final, true, []byte(payload))
		start := len(encoded) - len(payload)
		if data {
			spans = append(spans, span{len(wire) + start, len(wire) + len(encoded)})
		}
		frames = append(frames, frame{encoded, start, data})
		wire = append(wire, encoded...)
	}
	appendFrame(1, false, body[:len(body)/2], true)
	appendFrame(webSocketPing, true, "control", false)
	appendFrame(0, false, body[len(body)/2:], true)
	appendFrame(0, true, "", true)
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("ws-origin.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	for cut := 0; cut <= len(wire); cut++ {
		local, peer := net.Pipe()
		_ = local.SetDeadline(time.Now().Add(5 * time.Second))
		_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
		actual := tls.Client(local, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "ws-origin.example.test", RootCAs: roots})
		read := make(chan []byte, 1)
		originError := make(chan error, 1)
		written := make(chan struct{})
		allowClose := sync.OnceFunc(func() { close(written) })
		t.Cleanup(func() { _ = actual.NetConn().Close() })
		t.Cleanup(allowClose)
		go func() {
			server := tls.Server(peer, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*certificate}})
			defer server.NetConn().Close()
			if err := server.HandshakeContext(t.Context()); err != nil {
				originError <- err
				return
			}
			prefix := make([]byte, cut)
			_, err := io.ReadFull(server, prefix)
			read <- prefix
			originError <- err
			<-written
		}()
		if err := actual.HandshakeContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		meter := NewModelUsageMeterV1()
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "native", RequestBytes: uint64(len(body))}, 0)
		if err != nil || refusal != nil || turn.beginPhysicalWebSocketRequestV1() != nil {
			t.Fatal("original logical turn could not bind its physical writer", err)
		}
		state := &boundWebSocketState{ctx: t.Context(), origin: &physicalWSOriginCut{ReadWriteCloser: actual, remaining: cut}}
		for _, frame := range frames {
			var observed *ModelUsageTurnV1
			if frame.data {
				observed = turn
			}
			if err := state.emitOriginRequest(bytes.Clone(frame.wire), observed, frame.payloadStart); err != nil {
				break
			}
		}
		allowClose()
		if prefix := <-read; !bytes.Equal(prefix, wire[:cut]) {
			t.Fatalf("cut%d changed original native WS bytes", cut)
		}
		if err := <-originError; err != nil {
			t.Fatal(err)
		}
		_ = actual.NetConn().Close()
		turn.endPhysicalWebSocketRequestV1()
		var expected uint64
		for _, span := range spans {
			expected += uint64(max(0, min(cut, span.end)-span.start))
		}
		turn.mu.Lock()
		observed := turn.requestBytes
		turn.mu.Unlock()
		if observed != expected {
			t.Fatalf("cut%d counted%d request bytes, want%d native payload bytes", cut, observed, expected)
		}
		completion := turn.FinishV1(false)
		estimate, _ := modelUsageEstimateV1(expected, 0)
		if !completion.Estimated || completion.Counts.TotalTokens != estimate || completion.Counts.Requests != 1 {
			t.Fatalf("cut%d charged an inspected body, mask, control or unsent payload", cut)
		}
	}
}

func TestWebSocketTurnCannotSettleBeforeItsOriginalPhysicalRequestWriterJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		meter := NewModelUsageMeterV1()
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", RequestBytes: 4096}, 0)
		if err != nil || refusal != nil || turn.beginPhysicalWebSocketRequestV1() != nil {
			t.Fatal(err)
		}
		finished := make(chan ModelUsageCompletionV1, 1)
		go func() { finished <- turn.FinishV1(false) }()
		synctest.Wait()
		select {
		case <-finished:
			t.Fatal("meter settled before the original writer joined")
		default:
		}
		if meter.SnapshotV1().Total.Requests != 0 {
			t.Fatal("pending native request entered committed totals")
		}
		turn.physicalRequestPrefixV1(17)
		turn.endPhysicalWebSocketRequestV1()
		synctest.Wait()
		completion := <-finished
		if !completion.Estimated || completion.Counts.TotalTokens != 5 || completion.Counts.Requests != 1 {
			t.Fatal("writer join lost its final native prefix")
		}
	})
}

func TestActualTLSWebSocketOriginUsesOriginalFilteredMessageWriterAndTurn(t *testing.T) {
	body := `{"type":"response.create","stream_id":"main","model":"native","input":"` + strings.Repeat("x", 2*webSocketChunk) + `"}`
	const created = `{"type":"response.created","stream_id":"main","response":{"id":"native-response"}}`
	const terminal = `{"type":"response.completed","stream_id":"main","response":{"id":"native-response","usage":{"total_tokens":7}}}`
	uploaded := make(chan struct{})
	release := make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	t.Cleanup(open)
	originResult := make(chan error, 1)
	fixture := newPhysicalModelLifetimeFixture(t, false, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 || r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.ServerName != "api.openai.com" {
			originResult <- errPhysicalH2Binding
			return
		}
		conn, reader, err := modelUpgradeOrigin(w, r)
		if err != nil {
			originResult <- err
			return
		}
		defer conn.Close()
		ping, err := wsReadTestFrame(reader)
		if err != nil || ping.opcode != webSocketPing || string(ping.payload) != "control" {
			originResult <- errPhysicalH2Binding
			return
		}
		if _, err := conn.Write(wsTestWire(webSocketPong, true, false, ping.payload)); err != nil {
			originResult <- err
			return
		}
		request, err := wsReadTestMessage(reader)
		if err != nil || string(request) != body {
			originResult <- errPhysicalH2Binding
			return
		}
		close(uploaded)
		<-release
		for _, message := range []string{created, terminal} {
			if _, err := conn.Write(wsTestWire(1, true, false, []byte(message))); err != nil {
				originResult <- err
				return
			}
		}
		_, err = conn.Write(wsTestWire(webSocketClose, true, false, []byte{3, 232}))
		originResult <- err
	}))
	t.Cleanup(open) // release the fixture before its original connection join
	baseDial := fixture.env.srv.dialer.DialTCP
	scopes := make(chan *originRequest, 1)
	fixture.env.srv.dialer.DialTCP = func(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
		if scope, ok := ctx.Value(originRequestKey{}).(*originRequest); ok {
			scopes <- scope
		}
		return baseDial(ctx, addr)
	}
	transport := fixture.client.Transport.(*http.Transport)
	guest, err := transport.DialTLSContext(t.Context(), "tcp", "api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guest.Close() })
	if _, err := io.WriteString(guest, "GET /v1/responses HTTP/1.1\r\nHost: api.openai.com:443\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(guest)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil || response.StatusCode != 101 || response.Header.Get("Sec-WebSocket-Extensions") != "" {
		t.Fatal("actual checked TLS upgrade changed", err)
	}
	middle := len(body) / 2
	wire := append(wsTestWire(1, false, true, []byte(body[:middle])), wsTestWire(webSocketPing, true, true, []byte("control"))...)
	wire = append(wire, wsTestWire(0, true, true, []byte(body[middle:]))...)
	if _, err := guest.Write(wire); err != nil {
		t.Fatal(err)
	}
	pong, err := wsReadTestFrame(reader)
	if err != nil || pong.opcode != webSocketPong || string(pong.payload) != "control" {
		t.Fatal("actual control frame changed", err)
	}
	waitHTTPProgress(t, uploaded, "the original checked TLS origin's full logical message")
	var scope *originRequest
	select {
	case scope = <-scopes:
	case <-time.After(5 * time.Second):
		t.Fatal("actual checked origin scope missing")
	}
	usage := scope.usageWS
	usage.state.mu.Lock()
	if len(usage.state.records) != 1 {
		usage.state.mu.Unlock()
		t.Fatal("actual original logical turn missing")
	}
	turn := usage.state.records[0].usage
	usage.state.mu.Unlock()
	turn.mu.Lock()
	actual := turn.requestBytes
	physical := turn.physicalRequest
	turn.mu.Unlock()
	if !physical || actual != uint64(len(body)) {
		t.Fatal("actual direction bypassed its own native message observer or counted framing")
	}
	open()
	for _, message := range []string{created, terminal} {
		got, err := wsReadTestMessage(reader)
		if err != nil || string(got) != message {
			t.Fatal("actual TLS answer changed", err)
		}
	}
	closeFrame, err := wsReadTestFrame(reader)
	if err != nil || closeFrame.opcode != webSocketClose || len(closeFrame.payload) < 2 || binary.BigEndian.Uint16(closeFrame.payload) != 1000 {
		t.Fatal("actual TLS origin close changed", err)
	}
	if err := <-originResult; err != nil {
		t.Fatal(err)
	}
	snapshot := waitActualUsageRequests(t, fixture.env.srv, 1)
	if snapshot.Total.TotalTokens != 7 || snapshot.Total.EstimatedRequests != 0 || !api.ValidModelUsageV1(snapshot) || fixture.dials.Load() != 1 || fixture.guestDials.Load() != 1 {
		t.Fatal("actual original TLS WebSocket turn, Source or connection changed")
	}
}
