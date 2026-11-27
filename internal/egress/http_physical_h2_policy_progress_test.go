package egress

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalHTTP2StalledPolicyRetirementAllowsPermittedSiblingAndNewRequest(t *testing.T) {
	const prefix = `{"usage":{"total_tokens":`
	ready, release := make(chan struct{}), make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	t.Cleanup(open)
	fixture := newPhysicalModelLifetimeFixture(t, true, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("fresh") == "1" {
			_, _ = io.WriteString(w, prefix+"7}}")
			return
		}
		_, _ = io.WriteString(w, prefix)
		w.(http.Flusher).Flush()
		close(ready)
		select {
		case <-release:
			_, _ = io.WriteString(w, "7}}")
		case <-r.Context().Done():
		}
	}))
	baseDial := fixture.env.srv.dialer.DialTCP
	failed := make(chan *originRequest, 1)
	fixture.env.srv.dialer.DialTCP = func(ctx context.Context, address netip.AddrPort) (net.Conn, error) {
		scope, _ := ctx.Value(originRequestKey{}).(*originRequest)
		if scope != nil && scope.request.Path == "/v1/responses" {
			failed <- scope
			return nil, io.ErrUnexpectedEOF
		}
		return baseDial(ctx, address)
	}
	writer, events, native := physicalModelRawGuestConn(t, fixture, nil)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := func(path, body string) []hpack.HeaderField {
		return []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: "api.openai.com:443"}, {Name: ":path", Value: path}, {Name: "content-type", Value: "application/json"}, {Name: "content-length", Value: strconv.Itoa(len(body))}}
	}
	send := func(id uint32, path, body string) {
		if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: physicalH2Fields(t, encoder, &block, fields(path, body)), EndHeaders: true}); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteData(id, true, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	send(1, "/v1/responses", `{"model":"revoked","input":[]}`)
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
	}, "the original zero-window request returning before revocation")
	send(3, "/v1/chat/completions", `{"model":"permitted","messages":[]}`)
	if err := writer.WriteWindowUpdate(3, 65535); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, ready, "the original permitted sibling's streaming prefix")
	partial := physicalH2Fields(t, encoder, &block, fields("/v1/responses", ""))
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 5, BlockFragment: partial[:len(partial)/2], EndHeaders: false, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	waitHTTPState(t, func() bool {
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		return stream.owner.streams[5] != nil
	}, "the exposed header block which retains only the revoked request")
	updated := make(chan struct{})
	kept := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: false, Target: "api.openai.com/v1/responses"})
	go func() { defer close(updated); fixture.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: kept}) }()
	t.Cleanup(func() {
		open()
		_ = native.Close()
		select {
		case <-updated:
		case <-time.After(5 * time.Second):
			t.Error("original policy update did not join after releasing its owned gates and carrier")
		}
	})
	waitHTTPProgress(t, scope.ctx.Done(), "the original request's immediate revocation")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions?fresh=1", strings.NewReader(`{"model":"new-permitted","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal("stalled policy retirement held the global lock and blocked a permitted new request", err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || string(body) != prefix+"7}}" {
		t.Fatal("new permitted request did not finish during original retirement", readErr)
	}
	open()
	var sibling []byte
	for {
		select {
		case event := <-events:
			if event.err != nil || event.kind == http2.FrameGoAway {
				t.Fatal("stalled revocation killed its permitted original sibling", event.err)
			}
			if event.stream == 3 {
				sibling = append(sibling, event.payload...)
				if event.ended {
					goto progressed
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("permitted original sibling stalled behind revoked cleanup")
		}
	}
progressed:
	if string(sibling) != prefix+"7}}" || fixture.guestDials.Load() != 2 {
		t.Fatal("permitted progress changed payloads or replaced an original carrier")
	}
	select {
	case <-updated:
		t.Fatal("ordinary policy acknowledged before its exact revoked native owner finished")
	case <-scope.done:
		t.Fatal("revoked Source retired before abandoned native closure")
	default:
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, scope.done, "the revoked original native owner after actual closure")
	waitHTTPProgress(t, updated, "ordinary acknowledgement after exact original retirement")
}
