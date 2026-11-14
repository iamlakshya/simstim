package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type eofKeepalivePeer struct {
	mu      sync.Mutex
	calls   int
	bad     bool
	entered chan struct{}
	block   bool
}

func (*eofKeepalivePeer) HasFeature(string) bool { return true }
func (p *eofKeepalivePeer) Call(ctx context.Context, op string, args, out any) error {
	p.mu.Lock()
	p.calls++
	bad, block := p.bad, p.block
	p.mu.Unlock()
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	request := args.(api.PullReadArgs)
	value := api.PullChunk{Next: request.Offset, EOF: true}
	if bad {
		value.Data = []byte("unexpected")
	}
	return transferFixture(value, out)
}

func TestPullKeepaliveUsesOnlyExactEOFAndStopsBeforeRelease(t *testing.T) {
	peer := &eofKeepalivePeer{entered: make(chan struct{}, 1)}
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	owner := pullKeepaliveTicks(ctx, peer, api.PullExport{ID: "fixture2", Token: "abcdefghijklmnopqrstuvwxyz", Size: 19}, ticks, func() { close(stopped) }, cancel)
	ticks <- time.Now()
	<-peer.entered
	if err := owner.stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("keepalive ticker was not joined")
	}
	peer.mu.Lock()
	calls := peer.calls
	peer.mu.Unlock()
	ticks <- time.Now()
	peer.mu.Lock()
	after := peer.calls
	peer.mu.Unlock()
	if after != calls {
		t.Fatal("released export received another keepalive")
	}
}

func TestPullKeepaliveFailureCancelsLocalOperation(t *testing.T) {
	peer := &eofKeepalivePeer{bad: true, entered: make(chan struct{}, 1)}
	ticks := make(chan time.Time, 1)
	operation, cancel := context.WithCancel(t.Context())
	defer cancel()
	owner := pullKeepaliveTicks(operation, peer, api.PullExport{ID: "fixture2", Token: "abcdefghijklmnopqrstuvwxyz", Size: 19}, ticks, func() {}, cancel)
	ticks <- time.Now()
	<-operation.Done()
	if err := owner.stop(); err == nil {
		t.Fatal("malformed exact-EOF keepalive was ignored")
	}
}

func TestPullKeepaliveCancellationJoinsInFlightRead(t *testing.T) {
	peer := &eofKeepalivePeer{block: true, entered: make(chan struct{}, 1)}
	ticks := make(chan time.Time, 1)
	owner := pullKeepaliveTicks(t.Context(), peer, api.PullExport{ID: "fixture2", Token: "abcdefghijklmnopqrstuvwxyz", Size: 19}, ticks, func() {}, func() {})
	ticks <- time.Now()
	<-peer.entered
	if err := owner.stop(); err != nil {
		t.Fatal(err)
	}
}
