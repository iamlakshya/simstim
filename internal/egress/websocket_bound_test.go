package egress

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func wsPassFilter(_ context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
	return input, nil
}

func wsTestConnection(t *testing.T, filters webSocketFilters) (io.ReadWriteCloser, net.Conn, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	origin, peer := net.Pipe()
	if err := peer.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	wrapper, err := newBoundWebSocket(ctx, origin, filters)
	if err != nil {
		cancel()
		_ = origin.Close()
		_ = peer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = wrapper.Close(); _ = peer.Close() })
	return wrapper, peer, cancel
}

// This independent wire fixture deliberately does not use the output encoder.
func wsTestWire(opcode byte, final, masked bool, payload []byte) []byte {
	first, second := opcode, byte(0)
	if final {
		first |= 128
	}
	if masked {
		second |= 128
	}
	wire := []byte{first}
	switch {
	case len(payload) < 126:
		wire = append(wire, second|byte(len(payload)))
	case len(payload) <= 65535:
		wire = append(wire, second|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		wire = append(wire, second|127)
		var wide [8]byte
		binary.BigEndian.PutUint64(wide[:], uint64(len(payload)))
		wire = append(wire, wide[:]...)
	}
	mask := [4]byte{0x71, 0xb3, 0x05, 0xa9}
	if masked {
		wire = append(wire, mask[:]...)
	}
	for i, b := range payload {
		if masked {
			b ^= mask[i%4]
		}
		wire = append(wire, b)
	}
	return wire
}

type wsTestFrame struct {
	opcode        byte
	final, masked bool
	payload       []byte
}

func wsReadTestFrame(input io.Reader) (wsTestFrame, error) {
	var out wsTestFrame
	var head [2]byte
	if _, err := io.ReadFull(input, head[:]); err != nil {
		return out, err
	}
	out.opcode, out.final, out.masked = head[0]&15, head[0]&128 != 0, head[1]&128 != 0
	if head[0]&112 != 0 {
		return out, errors.New("output used a reserved bit")
	}
	length := uint64(head[1] & 127)
	if length == 126 {
		var size [2]byte
		if _, err := io.ReadFull(input, size[:]); err != nil {
			return out, err
		}
		length = uint64(binary.BigEndian.Uint16(size[:]))
	} else if length == 127 {
		var size [8]byte
		if _, err := io.ReadFull(input, size[:]); err != nil {
			return out, err
		}
		length = binary.BigEndian.Uint64(size[:])
	}
	if length > webSocketChunk {
		return out, errors.New("output exceeded its bounded frame")
	}
	var mask [4]byte
	if out.masked {
		if _, err := io.ReadFull(input, mask[:]); err != nil {
			return out, err
		}
	}
	out.payload = make([]byte, int(length))
	if _, err := io.ReadFull(input, out.payload); err != nil {
		return out, err
	}
	if out.masked {
		for i := range out.payload {
			out.payload[i] ^= mask[i%4]
		}
	}
	return out, nil
}

func wsReadTestMessage(input io.Reader) ([]byte, error) {
	var payload []byte
	for count := 0; count < 64; count++ {
		frame, err := wsReadTestFrame(input)
		if err != nil {
			return nil, err
		}
		if frame.opcode >= 8 {
			return nil, errors.New("unexpected control while reading data")
		}
		payload = append(payload, frame.payload...)
		if frame.final {
			return payload, nil
		}
	}
	return nil, errors.New("too many output frames for bounded fixture")
}

func wsRequireResult(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned fixture operation did not finish")
	}
}

func wsEchoFilter(t *testing.T, initial []responseEchoPair, register responseMintRegister) webSocketMessageFilter {
	t.Helper()
	var view atomic.Pointer[responseEchoSnapshot]
	snapshot, err := newResponseEchoSnapshot(initial)
	if err != nil {
		t.Fatal(err)
	}
	view.Store(snapshot)
	rules, err := newResponseRuleSnapshot([]responseMintRule{{ID: "websocket-finite-fixture", Expression: `\b(tok_[A-Z]{6})(?:["'\s;]|$)`}})
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
		return newResponseCredentialReader(ctx, input, func(context.Context) (*responseEchoSnapshot, error) { return view.Load(), nil }, rules,
			func(ctx context.Context, value api.Secret, id string) (api.Secret, error) {
				if register == nil {
					return "", errors.New("unexpected mint")
				}
				sentinel, err := register(ctx, value, id)
				if err != nil {
					return "", err
				}
				next, err := newResponseEchoSnapshot(append(append([]responseEchoPair(nil), initial...), responseEchoPair{Value: value, Sentinel: sentinel}))
				if err != nil {
					return "", err
				}
				view.Store(next)
				return sentinel, nil
			})
	}
}

func TestBoundWebSocketControlsProgressDuringWithheldEchoAcrossFragments(t *testing.T) {
	filter := wsEchoFilter(t, []responseEchoPair{{Value: "secret_ABCDE", Sentinel: "public_ABCDE"}}, nil)
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: filter, Client: wsPassFilter})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	written := make(chan error, 1)
	go func() {
		wire := append(wsTestWire(1, false, false, []byte("secret_")), wsTestWire(webSocketPing, true, false, []byte("ready"))...)
		if _, err := peer.Write(wire); err != nil {
			written <- err
			return
		}
		<-release
		_, err := peer.Write(wsTestWire(0, true, false, []byte("ABCDE")))
		written <- err
	}()
	control, err := wsReadTestFrame(wrapper)
	if err != nil || control.opcode != webSocketPing || string(control.payload) != "ready" {
		t.Fatal("ping stopped behind a withheld credential prefix")
	}
	// Guest upload must still progress while the server's message reader is
	// waiting for its continuation and neither direction closes the other.
	guestWritten := make(chan error, 1)
	go func() {
		_, err := wrapper.Write(wsTestWire(2, true, true, []byte("public_ABCDE")))
		guestWritten <- err
	}()
	guestPayload, err := wsReadTestMessage(peer)
	if err != nil || string(guestPayload) != "public_ABCDE" {
		t.Fatal("client payload was changed or injected")
	}
	wsRequireResult(t, guestWritten)
	once.Do(func() { close(release) })
	payload, err := wsReadTestMessage(wrapper)
	if err != nil || string(payload) != "public_ABCDE" {
		t.Fatal("fragmented server echo bypassed the canonical scanner")
	}
	wsRequireResult(t, written)
}

func TestBoundWebSocketFinitePatternWaitsForMessageNotFrameEOF(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var calls atomic.Int32
	filter := wsEchoFilter(t, nil, func(_ context.Context, value api.Secret, _ string) (api.Secret, error) {
		calls.Add(1)
		if value != "tok_ABCDEF" {
			return "", errors.New("wrong candidate")
		}
		return "tok_ZZZZZZ", nil
	})
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: filter, Client: wsPassFilter})
	written := make(chan error, 1)
	go func() {
		wire := append(wsTestWire(1, false, false, []byte("tok_ABCDEF")), wsTestWire(webSocketPong, true, false, []byte("wait"))...)
		if _, err := peer.Write(wire); err != nil {
			written <- err
			return
		}
		<-release
		wire = wsTestWire(0, true, false, []byte("A"))
		wire = append(wire, wsTestWire(1, true, false, []byte("tok_ABCDEF"))...)
		_, err := peer.Write(wire)
		written <- err
	}()
	control, err := wsReadTestFrame(wrapper)
	if err != nil || control.opcode != webSocketPong || calls.Load() != 0 {
		t.Fatal("frame EOF or control ended the data message")
	}
	releaseOnce.Do(func() { close(release) })
	first, err := wsReadTestMessage(wrapper)
	if err != nil || string(first) != "tok_ABCDEFA" {
		t.Fatal("continuation changed finite-pattern boundaries")
	}
	second, err := wsReadTestMessage(wrapper)
	if err != nil || string(second) != "tok_ZZZZZZ" || calls.Load() != 1 {
		t.Fatal("true message EOF did not use canonical mint publication")
	}
	wsRequireResult(t, written)
}

func TestBoundWebSocketMintPublicationPrecedesAnyReplacement(t *testing.T) {
	started, commit := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(commit) }) })
	filter := wsEchoFilter(t, nil, func(ctx context.Context, value api.Secret, _ string) (api.Secret, error) {
		close(started)
		select {
		case <-commit:
			return "tok_ZZZZZZ", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: filter, Client: wsPassFilter})
	written := make(chan error, 1)
	go func() { _, err := peer.Write(wsTestWire(1, true, false, []byte("tok_ABCDEF"))); written <- err }()
	read := make(chan []byte, 1)
	readError := make(chan error, 1)
	go func() { payload, err := wsReadTestMessage(wrapper); read <- payload; readError <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("mint registration did not start")
	}
	select {
	case <-read:
		t.Fatal("sentinel emitted before the canonical registration ACK")
	default:
	}
	once.Do(func() { close(commit) })
	select {
	case payload := <-read:
		if string(payload) != "tok_ZZZZZZ" {
			t.Fatal("wrong replacement")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("committed mint did not progress")
	}
	if err := <-readError; err != nil {
		t.Fatal(err)
	}
	wsRequireResult(t, written)
}

func TestBoundWebSocketControlEchoAndCloseReasonUseSameResponseFilter(t *testing.T) {
	filter := wsEchoFilter(t, []responseEchoPair{{Value: "secret_ABCDE", Sentinel: "public_ABCDE"}}, nil)
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: filter, Client: wsPassFilter})
	written := make(chan error, 1)
	go func() {
		closePayload := append([]byte{3, 232}, []byte("secret_ABCDE")...)
		wire := append(wsTestWire(webSocketPing, true, false, []byte("secret_ABCDE")), wsTestWire(webSocketClose, true, false, closePayload)...)
		_, err := peer.Write(wire)
		written <- err
	}()
	for _, opcode := range []byte{webSocketPing, webSocketClose} {
		frame, err := wsReadTestFrame(wrapper)
		if err != nil || frame.opcode != opcode {
			t.Fatal("control frame was lost")
		}
		payload := frame.payload
		if opcode == webSocketClose {
			if binary.BigEndian.Uint16(payload[:2]) != 1000 {
				t.Fatal("structural close status was changed")
			}
			payload = payload[2:]
		}
		if string(payload) != "public_ABCDE" {
			t.Fatal("control payload bypassed response scrubbing")
		}
	}
	wsRequireResult(t, written)
}

func TestBoundWebSocketBoundedBackpressureAndReverseDirection(t *testing.T) {
	wrapper, peer, cancel := wsTestConnection(t, webSocketFilters{Server: wsPassFilter, Client: wsPassFilter})
	threeFrames := make(chan struct{})
	originWrite := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			if _, err := peer.Write(wsTestWire(2, true, false, bytes.Repeat([]byte{'x'}, webSocketChunk))); err != nil {
				originWrite <- err
				return
			}
			if i == 1 {
				close(threeFrames)
			}
		}
		originWrite <- errors.New("unread guest allowed unbounded source prefetch")
	}()
	select {
	case <-threeFrames:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded output pipeline did not begin")
	}
	clientWrite := make(chan error, 1)
	go func() { _, err := wrapper.Write(wsTestWire(1, true, true, []byte("reverse"))); clientWrite <- err }()
	payload, err := wsReadTestMessage(peer)
	if err != nil || string(payload) != "reverse" {
		t.Fatal("blocked guest response stopped independent upload")
	}
	wsRequireResult(t, clientWrite)
	select {
	case err := <-originWrite:
		t.Fatalf("backpressure missing: %v", err)
	default:
	}
	cancel()
	closed := make(chan struct{})
	go func() { _ = wrapper.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join workers blocked on output backpressure")
	}
	select {
	case err := <-originWrite:
		if err == nil {
			t.Fatal("owned origin write remained successful")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned origin IO did not end")
	}
}

type wsErrorReadCloser struct{ err error }

func (r *wsErrorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (*wsErrorReadCloser) Close() error               { return nil }

func TestBoundWebSocketPolicyErrorsAreSafeCloseFrames(t *testing.T) {
	const private = "never-print-this-credential"
	reject := func(context.Context, byte, io.ReadCloser) (io.ReadCloser, error) {
		return &wsErrorReadCloser{err: errors.New(private)}, nil
	}
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: reject, Client: wsPassFilter})
	go func() { _, _ = peer.Write(wsTestWire(1, true, false, []byte(private))) }()
	frame, err := wsReadTestFrame(wrapper)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload[:2]) != 1008 || bytes.Contains(frame.payload, []byte(private)) {
		t.Fatal("unsafe or missing credential policy close")
	}
	var probe [1]byte
	_, err = wrapper.Read(probe[:])
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatal("terminal error leaked callback details")
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
		if strings.Contains(fmt.Sprintf(verb, wrapper), private) {
			t.Fatal("wrapper formatting leaked owned input")
		}
	}
}

func TestBoundWebSocketConstructorFailureDoesNotCloseCallerOrigin(t *testing.T) {
	origin, peer := net.Pipe()
	defer origin.Close()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newBoundWebSocket(ctx, origin, webSocketFilters{Server: wsPassFilter, Client: wsPassFilter}); err == nil {
		t.Fatal("cancelled constructor accepted")
	}
	written := make(chan error, 1)
	go func() { _, err := peer.Write([]byte{'x'}); written <- err }()
	var probe [1]byte
	if _, err := io.ReadFull(origin, probe[:]); err != nil || probe[0] != 'x' {
		t.Fatal("failed constructor took origin ownership")
	}
	wsRequireResult(t, written)
}

func TestBoundWebSocketCloseInterleavesWithUnfinishedCredentialPrefix(t *testing.T) {
	filter := wsEchoFilter(t, []responseEchoPair{{Value: "secret_ABCDE", Sentinel: "public_ABCDE"}}, nil)
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: filter, Client: wsPassFilter})
	written := make(chan error, 1)
	go func() {
		wire := append(wsTestWire(1, false, false, []byte("secret_")), wsTestWire(webSocketClose, true, false, []byte{3, 232})...)
		_, err := peer.Write(wire)
		written <- err
	}()
	frame, err := wsReadTestFrame(wrapper)
	if err != nil || frame.opcode != webSocketClose || !bytes.Equal(frame.payload, []byte{3, 232}) {
		t.Fatal("valid interleaved Close was masked by the response reader or leaked a pending prefix")
	}
	wsRequireResult(t, written)
	clientClose := make(chan error, 1)
	go func() {
		_, err := wrapper.Write(wsTestWire(webSocketClose, true, true, []byte{3, 232}))
		clientClose <- err
	}()
	returned, err := wsReadTestFrame(peer)
	if err != nil || returned.opcode != webSocketClose || !returned.masked || !bytes.Equal(returned.payload, []byte{3, 232}) {
		t.Fatal("close handshake did not preserve the opposite direction")
	}
	wsRequireResult(t, clientClose)
	var probe [1]byte
	if _, err := wrapper.Read(probe[:]); err != io.EOF {
		t.Fatal("completed close handshake was not a clean EOF")
	}
}

type wsObservedOrigin struct {
	net.Conn
	closed     atomic.Bool
	closeCount atomic.Int32
	reads      atomic.Int32
	target     int32
	reading    chan struct{}
}

func (c *wsObservedOrigin) Read(output []byte) (int, error) {
	if c.reads.Add(1) == c.target {
		close(c.reading)
	}
	return c.Conn.Read(output)
}
func (c *wsObservedOrigin) Close() error {
	c.closed.Store(true)
	c.closeCount.Add(1)
	return c.Conn.Close()
}

type wsWaitForCancellation struct {
	ctx     context.Context
	origin  *wsObservedOrigin
	ordered chan bool
	input   io.ReadCloser
}

func (r *wsWaitForCancellation) Read([]byte) (int, error) {
	<-r.ctx.Done()
	r.ordered <- r.origin.closed.Load()
	return 0, r.ctx.Err()
}
func (r *wsWaitForCancellation) Close() error { return r.input.Close() }

func TestBoundWebSocketCancellationClosesOriginBeforeFilterAndJoins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, peer := net.Pipe()
	defer peer.Close()
	origin := &wsObservedOrigin{Conn: conn}
	ready, ordered := make(chan struct{}), make(chan bool, 1)
	filter := func(ctx context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
		close(ready)
		return &wsWaitForCancellation{ctx: ctx, origin: origin, ordered: ordered, input: input}, nil
	}
	wrapper, err := newBoundWebSocket(ctx, origin, webSocketFilters{Server: filter, Client: wsPassFilter})
	if err != nil {
		t.Fatal(err)
	}
	defer wrapper.Close()
	go func() { _, _ = peer.Write(wsTestWire(2, true, false, nil)) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("message filter did not start")
	}
	cancel()
	select {
	case good := <-ordered:
		if !good {
			t.Fatal("filter cancellation preceded actual origin close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("filter did not observe cancellation")
	}
	joined := make(chan struct{})
	go func() { _ = wrapper.Close(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("owned workers did not join")
	}
	if origin.closeCount.Load() != 1 {
		t.Fatal("owned origin was closed more than once")
	}
}

func TestBoundWebSocketMessageInputCloseUnblocksItsActiveRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, peer := net.Pipe()
	defer peer.Close()
	origin := &wsObservedOrigin{Conn: conn, target: 2, reading: make(chan struct{})}
	inputView := make(chan io.ReadCloser, 1)
	filter := func(_ context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
		inputView <- input
		return input, nil
	}
	wrapper, err := newBoundWebSocket(ctx, origin, webSocketFilters{Server: filter, Client: wsPassFilter})
	if err != nil {
		t.Fatal(err)
	}
	defer wrapper.Close()
	// Declares a payload but supplies no bytes. This parks the real owner in
	// message.Read, rather than relying on a sleep or a guessed goroutine count.
	go func() { _, _ = peer.Write([]byte{0x82, 5}) }()
	var input io.ReadCloser
	select {
	case input = <-inputView:
	case <-time.After(5 * time.Second):
		t.Fatal("message input was not published")
	}
	select {
	case <-origin.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("message input did not enter its owned origin Read")
	}
	closed := make(chan struct{})
	go func() { _ = input.Close(); _ = wrapper.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("message Close failed to unblock and join the owned read")
	}
	if !origin.closed.Load() {
		t.Fatal("active message close left actual origin open")
	}
}

func TestBoundWebSocketTextReaderFormattingDoesNotCopyValueBuffers(t *testing.T) {
	const value = "private-input-buffer"
	reader := newWebSocketTextReader(strings.NewReader(value))
	var first [1]byte
	if _, err := reader.Read(first[:]); err != nil {
		t.Fatal(err)
	}
	for _, subject := range []any{reader, *reader} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
			if strings.Contains(fmt.Sprintf(verb, subject), value[1:]) {
				t.Fatal("text facade formatting exposed a retained raw input buffer")
			}
		}
	}
	reader.clear()
}

func TestBoundWebSocketEveryFragmentSplitUsesSameLogicalEchoContext(t *testing.T) {
	const value, sentinel = "secret_ABCDE", "public_ABCDE"
	for _, opcode := range []byte{1, 2} {
		for split := 0; split <= len(value); split++ {
			t.Run(fmt.Sprintf("type%d_split%d", opcode, split), func(t *testing.T) {
				filter := wsEchoFilter(t, []responseEchoPair{{Value: value, Sentinel: sentinel}}, nil)
				wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: filter, Client: wsPassFilter})
				written := make(chan error, 1)
				go func() {
					wire := append(wsTestWire(opcode, false, false, []byte(value[:split])), wsTestWire(0, true, false, []byte(value[split:]))...)
					_, err := peer.Write(wire)
					written <- err
				}()
				got, err := wsReadTestMessage(wrapper)
				if err != nil || string(got) != sentinel {
					t.Fatal("frame split bypassed exact server echo rewriting")
				}
				wsRequireResult(t, written)
			})
		}
	}
}

func TestBoundWebSocketEveryUTF8FragmentSplitPreservesText(t *testing.T) {
	const text = "a£€😀z"
	for split := 0; split <= len(text); split++ {
		t.Run(fmt.Sprintf("split%d", split), func(t *testing.T) {
			wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: wsPassFilter, Client: wsPassFilter})
			written := make(chan error, 1)
			go func() {
				wire := append(wsTestWire(1, false, false, []byte(text[:split])), wsTestWire(0, true, false, []byte(text[split:]))...)
				_, err := peer.Write(wire)
				written <- err
			}()
			got, err := wsReadTestMessage(wrapper)
			if err != nil || string(got) != text {
				t.Fatal("UTF-8 state reset at a continuation frame")
			}
			wsRequireResult(t, written)
		})
	}
}

func TestBoundWebSocketPingProgressDuringIncompleteUTF8Rune(t *testing.T) {
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: wsPassFilter, Client: wsPassFilter})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	written := make(chan error, 1)
	go func() {
		wire := append(wsTestWire(1, false, false, []byte{0xe2}), wsTestWire(webSocketPing, true, false, []byte("alive"))...)
		if _, err := peer.Write(wire); err != nil {
			written <- err
			return
		}
		<-release
		_, err := peer.Write(wsTestWire(0, true, false, []byte{0x82, 0xac}))
		written <- err
	}()
	control, err := wsReadTestFrame(wrapper)
	if err != nil || control.opcode != webSocketPing || string(control.payload) != "alive" {
		t.Fatal("UTF-8 carry blocked a valid control frame")
	}
	once.Do(func() { close(release) })
	payload, err := wsReadTestMessage(wrapper)
	if err != nil || string(payload) != "€" {
		t.Fatal("text rune was split or corrupted during control forwarding")
	}
	wsRequireResult(t, written)
}

func TestBoundWebSocketCanonicalFilterPreservesInputProtocolFailureCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire []byte
		code uint16
	}{
		{"invalid-text", wsTestWire(1, true, false, []byte{0xff}), 1007},
		{"bad-continuation", append(wsTestWire(2, false, false, nil), []byte{0xa0, 0}...), 1002},
		{"oversize-continuation", func() []byte {
			var header [10]byte
			header[0], header[1] = 0x80, 127
			binary.BigEndian.PutUint64(header[2:], maxDecodedHTTPBody+1)
			return append(wsTestWire(2, false, false, nil), header[:]...)
		}(), 1009},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := wsEchoFilter(t, nil, nil)
			wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: filter, Client: wsPassFilter})
			go func() { _, _ = peer.Write(tc.wire) }()
			frame, err := wsReadTestFrame(wrapper)
			if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload[:2]) != tc.code {
				t.Fatal("canonical filter masked a trusted framing/text failure code")
			}
		})
	}
}
