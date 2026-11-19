package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

func waitNewHostQuery(t *testing.T, entered <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-entered:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatal("local placement query did not start")
		return nil
	}
}

func TestCancelledDefaultLookupCannotOpenALateDialog(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	selected, err := client.ResolveHost(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan context.Context, 1), make(chan struct{})
	var releaseOnce sync.Once
	releaseQuery := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseQuery()
	hs.be.resolveHost = func(ctx context.Context, _ string) (client.Host, error) {
		entered <- ctx
		<-release // model an old worker returning a valid result after cancel
		return selected, nil
	}
	hs.start(hs.m.openQuickStart())
	ctx := waitNewHostQuery(t, entered)
	hs.deliver(keyMsg("esc"))
	if ctx.Err() == nil {
		t.Fatal("Escape left the local host query active")
	}
	releaseQuery()
	hs.do(nil)
	if hs.m.modal != nil || hs.m.prompt != nil || len(hs.be.newArgs) != 0 {
		t.Fatal("a cancelled default result changed the UI or started a session")
	}
}

func TestCancelledFormSelectionCannotStartAfterItsOwnerLeaves(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	hs.key("n")
	dialog := hs.m.modal.(*newSession)
	dialog.name.set("no-late-start")
	selected, err := client.ResolveHost(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan context.Context, 1), make(chan struct{})
	var releaseOnce sync.Once
	releaseQuery := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseQuery()
	hs.be.resolveHost = func(ctx context.Context, _ string) (client.Host, error) {
		entered <- ctx
		<-release
		return selected, nil
	}
	hs.start(dialog.submit(hs.m, false))
	ctx := waitNewHostQuery(t, entered)
	hs.deliver(keyMsg("esc"))
	if ctx.Err() == nil || hs.m.modal != nil {
		t.Fatal("Escape left the form or its local selection alive")
	}
	releaseQuery()
	hs.do(nil)
	if len(hs.be.newArgs) != 0 || len(hs.be.attached) != 0 || hs.m.modal != nil {
		t.Fatal("a retired form's local selection started or attached a session")
	}
}

func TestQueuedHostSelectionAfterCloseReadsNoConfigOrTransport(t *testing.T) {
	be := newFakeBackend()
	var calls atomic.Int32
	be.resolveHost = func(context.Context, string) (client.Host, error) { calls.Add(1); return client.Host{}, nil }
	model := New(t.Context(), be, Options{})
	command := model.openQuickStart()
	model.Close()
	if message := command(); message != nil || calls.Load() != 0 || len(be.dials) != 0 {
		t.Fatalf("closed selection ran: %T reads=%d dials=%v", message, calls.Load(), be.dials)
	}
}

func TestOlderLocalHostSelectionCannotReplaceANewerOne(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	old := hs.m.openQuickStart()
	current := hs.m.openQuickStart()
	if message := old(); message != nil {
		t.Fatalf("a cancelled queued selection still read the provider: %T", message)
	}
	hs.do(current)
	dialog, ok := hs.m.modal.(*newSession)
	if !ok || dialog.host != "gcp-dev" {
		t.Fatalf("current selection lost its dialog: %T", hs.m.modal)
	}
	// Delivering the old generation explicitly cannot close or replace it.
	hs.send(newHostResolvedMsg{seq: 1, host: client.Host{Name: "atlas-dev"}, action: newHostForm})
	if hs.m.modal != dialog {
		t.Fatal("a late old selection replaced the current dialog")
	}
}
