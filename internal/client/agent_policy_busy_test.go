//go:build darwin || linux

package client

import (
	"errors"
	"net"
	"testing"

	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/paths"
)

func TestBusyPostForgetOperationCannotRenewAnActiveAgentGrant(t *testing.T) {
	h, scope, source := channelHostFixture(t)
	a := bindChannelFixture(t, h, scope, source, channelTransport{})
	if err := a.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	connection := signChannel(t, paths.AgentSocket(scope.Session), source.public)
	defer connection.Close()
	ctx, release, err := LockHost(t.Context(), h.Name, "host rm")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := UpdateHosts(ctx, HostChange{Kind: "host rm", Was: &h}); err != nil {
		t.Fatal(err)
	}
	// Real host rm still owns this operation lock while printing or
	// recording its completed removal. The attach has a different context.
	watcher := &attacher{agent: a, o: AttachOptions{Host: h, Session: scope.Session}}
	err = watcher.watchAgentGrant(t.Context())
	var busy *UserError
	if !errors.As(err, &busy) || busy.Code != "host_busy" {
		t.Fatalf("active fresh-policy failure = %v, want the busy operation refusal", err)
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("a removed host kept its active agent capability while the operation lock stayed busy")
	}
	accepts, lists, signs := source.accepts.Load(), source.agent.lists.Load(), source.agent.signs.Load()
	_, err = agent.NewClient(connection).List()
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("the old signing FD served or hung after the failed policy check: %v", err)
	}
	if source.accepts.Load() != accepts || source.agent.lists.Load() != lists || source.agent.signs.Load() != signs {
		t.Fatal("a stale grant reached the source agent after committed removal")
	}
}
