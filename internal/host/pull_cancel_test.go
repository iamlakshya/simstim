package host

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

type cancelOutput struct {
	done chan struct{}
	once sync.Once
}

func (*cancelOutput) Write(p []byte) (int, error) { return len(p), nil }
func (o *cancelOutput) Cancel() error             { o.once.Do(func() { close(o.done) }); return nil }
func (o *cancelOutput) Wait() error               { <-o.done; return nil }

func TestAbortAdmissionDoesNotWaitForProgressingOutputDelivery(t *testing.T) {
	rpc, err := NewPullRPC(t.Context(), func(context.Context, string, string) (pullexport.Source, error) {
		t.Fatal("abort allocated a source")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := &bridgePull{PullRPC: rpc, routes: map[string]*pullRoute{}, aliases: map[string]*pullRoute{}, pending: map[uint64]*pullPending{}, relays: map[string]*LivePullRelay{}}
	output := &cancelOutput{done: make(chan struct{})}
	b := &bridge{pull: owner, ownedOutput: output, w: proto.NewWriter(io.Discard)}
	t.Cleanup(b.closePull)
	token := strings.Repeat("a", 26)
	route := &pullRoute{original: "fixture2", primary: "primary2", token: token}
	owner.routes[route.primary] = route
	owner.aliases[token] = route
	data, err := json.Marshal(api.PulledArgs{ID: route.original, Token: token, Abort: true})
	if err != nil {
		t.Fatal(err)
	}
	owner.delivery.Lock()
	returned := make(chan error, 1)
	go func() {
		run, _, err := b.preparePull(proto.Request{ID: 0, Op: api.OpPulled, Args: data})
		if err == nil && run == nil {
			err = pullRouteError("abort has no owned completion")
		}
		returned <- err
	}()
	select {
	case err := <-returned:
		owner.delivery.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		owner.delivery.Unlock()
		t.Fatal("abort waited behind an unrelated progressing response frame")
	}
	if route.finishing != 1 {
		t.Fatal("abort did not fence future output progress before forwarding")
	}
}
