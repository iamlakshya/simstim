package egress

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

func holdWaitModes(t *testing.T, run func(*testing.T)) {
	t.Helper()
	for _, mode := range []string{"socketpair", "pipe"} {
		t.Run(mode, func(t *testing.T) {
			value := ""
			if mode == "pipe" {
				value = "1"
			}
			t.Setenv("LINK_TEST_PIPE", value)
			run(t)
		})
	}
}

func newHoldWaitFixture(t *testing.T) (*Server, *link.Session) {
	t.Helper()
	a, b := holdTransportPair(t)
	server, err := NewServer(b, Options{Dialer: &Dialer{}})
	if err != nil {
		t.Fatal(err)
	}
	peer := link.New(a, link.Supervisor, LinkOptions())
	if _, err := server.initialiseControl(memberJSON(t, InitArgs{SessionID: "waiting2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk, Rules: []api.Rule{{Layer: api.LayerSession, Target: "unrelated.example", Allow: true, Once: true, GrantID: "unrelated"}}}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	return server, peer
}

func openHoldWaitStream(t *testing.T, server *Server, peer *link.Session) (*link.Stream, *link.Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out, err := peer.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "authored.example", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	in, err := server.sess.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = out.SetDeadline(time.Now().Add(5 * time.Second))
	_ = in.SetDeadline(time.Now().Add(5 * time.Second))
	return out, in
}

func TestHeldWaiterActualStreamAbandonRemovesWaiterButRetainsFullQuestion(t *testing.T) {
	holdWaitModes(t, func(t *testing.T) {
		server, peer := newHoldWaitFixture(t)
		out, in := openHoldWaitStream(t, server, peer)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		entered := make(chan struct{}, 1)
		result := make(chan Decision, 1)
		var workers sync.WaitGroup
		workers.Go(func() {
			result <- server.awaitUnknown(ctx, Request{Host: "authored.example", Port: 443}, "authored.example", "explicit", in, func(engine *Engine, request Request) Decision {
				select {
				case entered <- struct{}{}:
				default:
				}
				return engine.evaluate(request, nil, evalPrecheck)
			})
		})
		defer func() { cancel(); workers.Wait() }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("actual wait probe did not enter")
		}
		// The real gate is the registration barrier: the probe sent entered
		// while holding it, and attachment precedes its release.
		server.policyMu.Lock()
		before := server.pendingNetworkSnapshot()
		server.policyMu.Unlock()
		if len(before.Requests) != 1 || before.Requests[0].Waiting != 1 {
			t.Fatal("actual wait did not register its exact flow")
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case decision := <-result:
			if decision.pendingRefusal != heldRefusalCancelled {
				t.Fatal("peer full-close was not exact waiter cancellation")
			}
		case <-ctx.Done():
			t.Fatal("abandoned actual stream kept its ghost waiter")
		}
		after := server.pendingNetworkSnapshot()
		if len(after.Requests) != 1 || after.Requests[0].ID != before.Requests[0].ID || after.Requests[0].Waiting != 0 || !after.Requests[0].Expires.Equal(before.Requests[0].Expires) {
			t.Fatal("flow cancellation removed or renewed the held request")
		}
	})
}

func TestHeldWaiterActualHalfClosePreservesExactResponseAndNoPermission(t *testing.T) {
	holdWaitModes(t, func(t *testing.T) {
		server, peer := newHoldWaitFixture(t)
		out, in := openHoldWaitStream(t, server, peer)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		entered := make(chan struct{}, 1)
		result := make(chan Decision, 1)
		var workers sync.WaitGroup
		workers.Go(func() {
			result <- server.awaitUnknown(ctx, Request{Host: "authored.example", Port: 443}, "authored.example", "explicit", in, func(engine *Engine, request Request) Decision {
				select {
				case entered <- struct{}{}:
				default:
				}
				return engine.evaluate(request, nil, evalPrecheck)
			})
		})
		defer func() { cancel(); workers.Wait() }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("actual wait did not enter")
		}
		server.policyMu.Lock()
		before := snapshotDNSLedger(server.currentEngine())
		snapshot := server.pendingNetworkSnapshot()
		server.policyMu.Unlock()
		if len(snapshot.Requests) != 1 {
			t.Fatal("real question missing")
		}
		if err := out.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(in); err != nil {
			t.Fatal(err)
		}
		select {
		case <-in.Done():
			t.Fatal("request half-close abandoned its response")
		default:
		}
		select {
		case <-result:
			t.Fatal("half-close cancelled the real waiter")
		default:
		}
		const response = "owned response bytes\x00\xff"
		if _, err := io.WriteString(in, response); err != nil {
			t.Fatal(err)
		}
		if err := in.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(out)
		if err != nil || string(got) != response {
			t.Fatal("lifecycle observer changed exact response bytes", err)
		}
		if !reflect.DeepEqual(before, snapshotDNSLedger(server.currentEngine())) || server.currentEngine().Decide(Request{Host: "authored.example", Port: 443}).Allow {
			t.Fatal("waiting/half-close manufactured a permission")
		}
		cancel()
		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("owned waiter did not leave after context cancellation")
		}
		_ = out.Close()
		_ = in.Close()
	})
}
