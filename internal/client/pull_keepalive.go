package client

import (
	"context"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// A completed download may spend time in native Git and static review. The
// exact-EOF read renews only this owned export and carries no new bytes.
const pullKeepaliveEvery = 30 * time.Second

type pullKeepalive struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	failure error
}

func startPullKeepalive(ctx context.Context, peer pullPeer, export api.PullExport, failed context.CancelFunc) *pullKeepalive {
	ticker := time.NewTicker(pullKeepaliveEvery)
	return pullKeepaliveTicks(ctx, peer, export, ticker.C, ticker.Stop, failed)
}

func pullKeepaliveTicks(ctx context.Context, peer pullPeer, export api.PullExport, ticks <-chan time.Time, stop func(), failed context.CancelFunc) *pullKeepalive {
	life, cancel := context.WithCancel(ctx)
	owner := &pullKeepalive{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(owner.done)
		defer stop()
		for {
			select {
			case <-life.Done():
				return
			case _, ok := <-ticks:
				if !ok {
					return
				}
				var chunk api.PullChunk
				err := peer.Call(life, api.OpPullRead, api.PullReadArgs{ID: export.ID, Token: export.Token, Offset: export.Size}, &chunk)
				if life.Err() != nil {
					return
				}
				if err == nil && (len(chunk.Data) != 0 || chunk.Next != export.Size || !chunk.EOF) {
					err = pullProtocol("the export keepalive did not return exact EOF")
				}
				if err != nil {
					owner.mu.Lock()
					owner.failure = err
					owner.mu.Unlock()
					failed()
					return
				}
			}
		}
	}()
	return owner
}

func (p *pullKeepalive) stop() error {
	if p == nil {
		return nil
	}
	p.cancel()
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}
