package egress

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalHTTP2CancelledPolicyJoinReportsIncompleteAndRetainsOriginalSource(t *testing.T) {
	fixture := newPhysicalModelLifetimeFixture(t, true, true, http.NotFoundHandler())
	failed := make(chan *originRequest, 1)
	fixture.env.srv.dialer.DialTCP = func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
		scope, _ := ctx.Value(originRequestKey{}).(*originRequest)
		failed <- scope
		return nil, io.ErrUnexpectedEOF
	}
	writer, _, native := physicalModelRawGuestConn(t, fixture, nil)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	const body = `{"model":"revoked","input":[]}`
	fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: "api.openai.com:443"}, {Name: ":path", Value: "/v1/responses"}, {Name: "content-type", Value: "application/json"}, {Name: "content-length", Value: strconv.Itoa(len(body))}}
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteData(1, true, []byte(body)); err != nil {
		t.Fatal(err)
	}
	var scope *originRequest
	select {
	case scope = <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("original revoked request did not start")
	}
	stream := scope.physicalDelivery
	waitHTTPState(t, func() bool {
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		return stream.returned && !stream.settled
	}, "the original response buffered behind its zero window")
	partial := physicalH2Fields(t, encoder, &block, fields)
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 3, BlockFragment: partial[:len(partial)/2], EndHeaders: false, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	waitHTTPState(t, func() bool {
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		return stream.owner.streams[3] != nil
	}, "the actual exposed incomplete header block")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	kept := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: false, Target: "api.openai.com/v1/responses"})
	result := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, err := fixture.env.srv.replaceControlPolicyContext(ctx, memberJSON(t, PolicyUpdateArgs{Policy: kept}))
		result <- err
	}()
	t.Cleanup(func() {
		cancel()
		_ = native.Close()
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Error("cancelled policy caller did not join after owned teardown")
		}
	})
	waitHTTPProgress(t, scope.ctx.Done(), "the exact original scope revoking before caller cancellation")
	cancel()
	select {
	case err := <-result:
		var detail *proto.Error
		if !errors.As(err, &detail) || detail.Code != "policy_cleanup_incomplete" || !strings.Contains(detail.Message, "cleanup has not finished") || detail.Hint == "" {
			t.Fatal("cancelled ordinary policy wait reported a successful or unactionable acknowledgement", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary policy retirement ignored caller cancellation")
	}
	select {
	case <-scope.done:
		t.Fatal("caller cancellation fabricated original native retirement")
	default:
	}
	stream.owner.mu.Lock()
	closed, finished := stream.owner.closed, stream.finished
	stream.owner.mu.Unlock()
	if closed || finished || len(fixture.env.srv.httpSem) != 1 || len(fixture.env.srv.flows.snapshot()) != 1 {
		t.Fatal("cancelled policy wait discarded original ownership or killed the carrier")
	}
	if fixture.env.srv.currentEngine().Decide(Request{Host: "api.openai.com", Port: 443, Path: "/v1/responses", Method: http.MethodPost}).Allow {
		t.Fatal("cancelled cleanup wait undid the already published policy")
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, scope.done, "original cleanup after later physical socket closure")
}
