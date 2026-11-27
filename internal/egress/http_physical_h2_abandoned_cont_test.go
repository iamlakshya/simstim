package egress

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalHTTP2AbandonedExposedContinuationRetainsRevokedSourceUntilNativeClosure(t *testing.T) {
	fixture := newPhysicalModelLifetimeFixture(t, true, true, http.NotFoundHandler())
	original := make(chan *originRequest, 1)
	fixture.env.srv.dialer.DialTCP = func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
		scope, _ := ctx.Value(originRequestKey{}).(*originRequest)
		original <- scope
		return nil, io.ErrUnexpectedEOF
	}
	writer, events, native := physicalModelRawGuestConn(t, fixture, nil)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	const body = `{"model":"model-a","input":[]}`
	fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: "api.openai.com:443"}, {Name: ":path", Value: "/v1/responses"}, {Name: "content-type", Value: "application/json"}, {Name: "content-length", Value: strconv.Itoa(len(body))}}
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteData(1, true, []byte(body)); err != nil {
		t.Fatal(err)
	}
	var scope *originRequest
	select {
	case scope = <-original:
		if scope == nil || scope.physicalDelivery == nil {
			t.Fatal("original production request lost physical ownership")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original production scope did not start")
	}
	stream := scope.physicalDelivery
	waitHTTPState(t, func() bool {
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		return stream.returned && !stream.settled
	}, "the original returned request with a zero-window refusal")
	// Expose a second request's HEADERS to the same original server, then
	// abandon its CONTINUATION. The server may not see a private RST inside
	// this block. No origin request for stream 3 can have been admitted yet.
	second := physicalH2Fields(t, encoder, &block, fields)
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 3, BlockFragment: second[:len(second)/2], EndHeaders: false, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	waitHTTPState(t, func() bool {
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		return stream.owner.streams[3] != nil
	}, "the original exposed but incomplete header block")
	updated := make(chan struct{})
	kept := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: false, Target: "api.openai.com/v1/responses"})
	go func() {
		defer close(updated)
		fixture.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: kept})
	}()
	t.Cleanup(func() {
		_ = native.Close()
		select {
		case <-updated:
		case <-time.After(5 * time.Second):
			t.Error("original policy update did not join after owned native teardown")
		}
	})
	waitHTTPProgress(t, scope.ctx.Done(), "revocation cancelling the original scope immediately")
	hold := time.NewTimer(100 * time.Millisecond)
	defer hold.Stop()
	for {
		select {
		case <-scope.done:
			t.Fatal("revoked Source retired inside an exposed CONTINUATION chain")
		case <-updated:
			t.Fatal("policy acknowledged before native closure of the abandoned chain")
		case event := <-events:
			if event.err != nil || event.kind == http2.FrameGoAway {
				t.Fatal("revoking one request terminated the native carrier inside an unfinished header block", event.err)
			}
		case <-hold.C:
			goto retained
		}
	}
retained:
	stream.owner.mu.Lock()
	finished, closed, serverClosed := stream.finished, stream.owner.closed, stream.serverClosed
	stream.owner.mu.Unlock()
	if finished || closed || serverClosed || len(fixture.env.srv.httpSem) != 1 || len(fixture.env.srv.flows.snapshot()) != 1 || fixture.env.srv.modelUsageSnapshot().Total.Requests != 0 {
		t.Fatal("abandoned header cancellation released or killed original ownership early")
	}
	if fixture.dials.Load() != 0 || fixture.guestDials.Load() != 1 {
		t.Fatal("incomplete headers reached an origin or rotated the carrier")
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, scope.done, "the original Source after actual native closure and write join")
	waitHTTPProgress(t, updated, "policy acknowledgement after original physical closure")
	waitHTTPState(t, func() bool {
		return len(fixture.env.srv.httpSem) == 0 && len(fixture.env.srv.flows.snapshot()) == 0 && len(fixture.env.srv.originSem) == 0
	}, "all original request ownership after abandoned-chain closure")
	usage := fixture.env.srv.modelUsageSnapshot()
	if usage.Total.Requests != 1 || usage.Total.EstimatedRequests != 1 {
		t.Fatal("abandoned-chain closure lost or duplicated the original meter turn")
	}
}
