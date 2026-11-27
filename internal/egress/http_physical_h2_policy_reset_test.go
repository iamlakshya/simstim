package egress

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func physicalModelRawGuestConn(t *testing.T, fixture *httpLifetimeFixture, gate <-chan struct{}) (*http2.Framer, <-chan physicalH2TestEvent, net.Conn) {
	t.Helper()
	transport := fixture.client.Transport.(*http.Transport)
	conn, err := transport.DialTLSContext(t.Context(), "tcp", "api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	events := make(chan physicalH2TestEvent, 11000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		reader := http2.NewFramer(io.Discard, conn)
		reader.ReadMetaHeaders = hpack.NewDecoder(physicalH2OriginalTableLimit, nil)
		reader.MaxHeaderListSize = physicalH2OriginalHeaderLimit
		first := true
		for {
			if !first && gate != nil {
				<-gate
			}
			first = false
			frame, err := reader.ReadFrame()
			if err != nil {
				events <- physicalH2TestEvent{err: err}
				return
			}
			event := physicalH2TestEvent{kind: frame.Header().Type, stream: frame.Header().StreamID}
			switch frame := frame.(type) {
			case *http2.DataFrame:
				event.ended, event.payload = frame.StreamEnded(), bytes.Clone(frame.Data())
			case *http2.MetaHeadersFrame:
				event.ended = frame.StreamEnded()
			case *http2.PingFrame:
				event.payload = bytes.Clone(frame.Data[:])
				event.ended = frame.Flags.Has(http2.FlagPingAck)
			}
			events <- event
		}
	}()
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	writer := http2.NewFramer(conn, nil)
	if err := writer.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.err != nil || event.kind != http2.FrameSettings {
			t.Fatal("original native HTTP2 settings missing", event.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original native HTTP2 handshake stalled")
	}
	if err := writer.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("original guest TLS reader did not join")
		}
	})
	return writer, events, conn
}

func TestPhysicalHTTP2PolicyRetiresQueuedZeroWindowRefusalAndKeepsSiblingThroughFullControlBudget(t *testing.T) {
	const pingCount = 10000 // the original server's complete control queue budget
	const prefix = `{"usage":{"total_tokens":`
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	originReady := make(chan struct{})
	fixture := newPhysicalModelLifetimeFixture(t, true, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, prefix)
		w.(http.Flusher).Flush()
		close(originReady)
		select {
		case <-release:
			_, _ = io.WriteString(w, "7}}")
		case <-r.Context().Done():
		}
	}))
	baseDial := fixture.env.srv.dialer.DialTCP
	failedScope := make(chan *originRequest, 1)
	fixture.env.srv.dialer.DialTCP = func(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
		scope, _ := ctx.Value(originRequestKey{}).(*originRequest)
		if scope != nil && scope.request.Path == "/v1/responses" {
			failedScope <- scope
			return nil, io.ErrUnexpectedEOF
		}
		return baseDial(ctx, addr)
	}
	gate := make(chan struct{})
	openGate := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(openGate)
	writer, events, native := physicalModelRawGuestConn(t, fixture, gate)
	// Ensure the read gate opens before that helper joins its native reader.
	t.Cleanup(openGate)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	send := func(id uint32, path, body string) {
		fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: "api.openai.com:443"}, {Name: ":path", Value: path}, {Name: "content-type", Value: "application/json"}, {Name: "content-length", Value: strconv.Itoa(len(body))}}
		if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true}); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteData(id, true, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	send(1, "/v1/responses", `{"model":"model-a","input":[]}`)
	var scope *originRequest
	select {
	case scope = <-failedScope:
	case <-time.After(5 * time.Second):
		t.Fatal("original failed-dial scope missing")
	}
	stream := scope.physicalDelivery
	if stream == nil {
		t.Fatal("actual proxy request lost its original physical stream")
	}
	waitHTTPState(t, func() bool {
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		return stream.returned && !stream.settled
	}, "the original returned handler with its refusal still behind a zero stream window")
	if fixture.env.srv.modelUsageSnapshot().Total.Requests != 0 {
		t.Fatal("buffered local refusal released its original turn")
	}
	send(3, "/v1/chat/completions", `{"model":"model-b","messages":[]}`)
	if err := writer.WriteWindowUpdate(3, 65535); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, originReady, "the permitted sibling's original upstream prefix")
	for index := 0; index < pingCount; index++ {
		var data [8]byte
		binary.BigEndian.PutUint64(data[:], uint64(index))
		if err := writer.WritePing(false, data); err != nil {
			t.Fatal("original control budget was reduced before cancellation", err)
		}
	}
	updated := make(chan struct{})
	kept := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: false, Target: "api.openai.com/v1/responses"})
	go func() { defer close(updated); fixture.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: kept}) }()
	t.Cleanup(func() {
		openGate()
		once.Do(func() { close(release) })
		_ = native.Close()
		select {
		case <-updated:
		case <-time.After(5 * time.Second):
			t.Error("original full-control-budget policy caller did not join after owned teardown")
		}
	})
	waitHTTPState(t, func() bool { stream.owner.mu.Lock(); defer stream.owner.mu.Unlock(); return stream.resetRequested }, "the original policy cancellation's own stream reset")
	select {
	case <-updated:
		t.Fatal("policy acknowledged before original native reset bytes could cross the gated guest writer")
	default:
	}
	select {
	case <-scope.done:
		t.Fatal("Source/permit owner ended before its original native reset")
	default:
	}
	stream.owner.mu.Lock()
	privatePings := len(stream.owner.fences)
	stream.owner.mu.Unlock()
	if privatePings != 0 {
		t.Fatal("private fences consumed the original control queue budget")
	}
	openGate()
	seenPings := make(map[uint64]bool)
	reset, gotPrefix := false, false
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for len(seenPings) != pingCount || !reset || !gotPrefix {
		select {
		case event := <-events:
			if event.err != nil {
				t.Fatal("retiring one request closed the permitted original carrier", event.err)
			}
			switch event.kind {
			case http2.FramePing:
				if len(event.payload) != 8 || !event.ended {
					t.Fatal("guest PING identity changed")
				}
				id := binary.BigEndian.Uint64(event.payload)
				if id >= pingCount || seenPings[id] {
					t.Fatal("private or duplicate PING ACK escaped")
				}
				seenPings[id] = true
			case http2.FrameRSTStream:
				if event.stream == 1 {
					reset = true
				} else {
					t.Fatal("policy reset selected its permitted sibling")
				}
			case http2.FrameData:
				if event.stream == 1 {
					t.Fatal("zero-window local refusal DATA crossed after cancellation")
				}
				if event.stream == 3 && string(event.payload) == prefix {
					gotPrefix = true
				}
			}
		case <-deadline.C:
			t.Fatal("original control budget, reset or permitted sibling did not drain")
		}
	}
	waitHTTPProgress(t, updated, "the original policy hash only after physical stream retirement")
	once.Do(func() { close(release) })
	for {
		select {
		case event := <-events:
			if event.err != nil {
				t.Fatal("permitted sibling was lost after policy acknowledgement", event.err)
			}
			if event.stream == 3 && event.ended {
				goto finished
			}
		case <-deadline.C:
			t.Fatal("same-connection sibling never completed")
		}
	}
finished:
	waitHTTPState(t, func() bool { return fixture.env.srv.modelUsageSnapshot().Total.Requests == 2 }, "both original meter finalisers")
	usage := fixture.env.srv.modelUsageSnapshot()
	if usage.Total.TotalTokens != 7 || usage.Total.EstimatedRequests != 1 || fixture.guestDials.Load() != 1 {
		t.Fatal("retirement changed counters or transparently reconnected", fmt.Sprint(usage.Total))
	}
	if scope.ctx.Err() == nil {
		t.Fatal("original failed request scope remained live after joined retirement")
	}
}
