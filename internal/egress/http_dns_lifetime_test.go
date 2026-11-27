package egress

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTP2PendingDNSCancellationDoesNotCloseItsPermittedSibling(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	pol := onceTestPolicy(t, api.PresetLocked,
		api.Rule{Allow: true, Target: "api.example.com/pending", Once: true, GrantID: "dns-instance"},
		api.Rule{Allow: true, Target: "api.example.com/held"})
	f := newHTTPLifetimeFixture(t, pol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "open")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, "survived")
		case <-r.Context().Done():
		}
	}), nil)
	held, err := f.client.Get("https://api.example.com/held")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Body.Close()
	requireHTTPPrefix(t, held, "open")
	lookup, cancelled := make(chan struct{}), make(chan struct{})
	// The first request has completed its checked lookup and origin TLS.
	// The next fresh request dial must keep its own cancellation even when
	// the standard Transport detaches the handler context from a dial.
	f.env.srv.dialer.Lookup = func(ctx context.Context, _ string) ([]netip.Addr, error) {
		close(lookup)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, _ := f.client.Get("https://api.example.com/pending")
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	waitHTTPProgress(t, lookup, "the second request's pending DNS")
	kept := pol
	kept.Rules = nil
	for _, rule := range pol.Rules {
		if rule.Target == "api.example.com/held" {
			kept.Rules = append(kept.Rules, rule)
		}
	}
	f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: kept})
	waitHTTPProgress(t, cancelled, "the exact pending dial's cancellation")
	waitHTTPProgress(t, done, "the cancelled pending request to return")
	releaseOnce.Do(func() { close(release) })
	if rest, err := io.ReadAll(held.Body); err != nil || string(rest) != "survived" {
		t.Fatalf("pending DNS cancellation closed a permitted sibling: %q, %v", rest, err)
	}
	if f.dials.Load() != 1 || f.guestDials.Load() != 1 {
		t.Fatalf("cancelled DNS dialled or replaced a connection: origins=%d guests=%d", f.dials.Load(), f.guestDials.Load())
	}
}
