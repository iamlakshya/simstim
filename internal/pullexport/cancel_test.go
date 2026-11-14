package pullexport

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func TestAbortWhileBeginPendingCancelsAndJoinsExactlyOneSource(t *testing.T) {
	started := make(chan CaptureArgs, 1)
	source := &exportFixture{closed: make(chan struct{}), capture: func(ctx context.Context, args CaptureArgs, _ io.Writer) (CaptureResult, error) {
		started <- args
		<-ctx.Done()
		return CaptureResult{}, ctx.Err()
	}}
	manager, clock, _ := managerFixture(t, source)
	args := api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}
	beginDone := make(chan error, 1)
	go func() { _, err := manager.Begin(t.Context(), "outer-owner", "primary2", args, 1<<20); beginDone <- err }()
	preparing := <-started
	// The caller knows this nonce before begin's reply exists. A normal
	// concurrent RPC can abort it without waiting for that reply.
	if _, err := manager.Finish(t.Context(), "outer-owner", api.PulledArgs{ID: args.ID, Token: args.Token, Abort: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-beginDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("pending begin: %v", err)
	}
	clock.advance(time.Hour)
	preparing.Progress() // even a queued heartbeat cannot revive a closed task
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closes != 1 || len(source.recorded) != 0 {
		t.Fatal("abort did not join once, or recorded work")
	}
}

func TestLongCaptureProgressIsScopedToSameTaskAndClientLifetime(t *testing.T) {
	started := make(chan CaptureArgs, 1)
	finish := make(chan struct{})
	source := &exportFixture{closed: make(chan struct{}), capture: func(ctx context.Context, args CaptureArgs, out io.Writer) (CaptureResult, error) {
		started <- args
		select {
		case <-finish:
		case <-ctx.Done():
			return CaptureResult{}, ctx.Err()
		}
		ref := "refs/heads/" + brand.Name + "/primary2/topic"
		_, err := io.WriteString(out, "# v2 git bundle\n"+strings.Repeat("b", 40)+" "+ref+"\n\nPACKfixture")
		return CaptureResult{DefaultRef: ref}, err
	}}
	manager, clock, _ := managerFixture(t, source)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	args := api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}
	beginDone := make(chan error, 1)
	go func() { _, err := manager.Begin(ctx, "outer-owner", "primary2", args, 1<<20); beginDone <- err }()
	active := <-started
	current, err := manager.find("outer-owner", args.ID, args.Token)
	if err != nil {
		t.Fatal(err)
	}
	for range 16 {
		clock.advance(time.Minute)
		active.Progress()
		current.mu.Lock()
		last := current.last
		current.mu.Unlock()
		if !last.Equal(clock.read()) {
			t.Fatal("healthy exact capture hit a whole-operation deadline")
		}
	}
	close(finish)
	if err = <-beginDone; err != nil {
		t.Fatal(err)
	}
	current.mu.Lock()
	last := current.last
	current.mu.Unlock()
	clock.advance(time.Minute)
	active.Progress() // capture ended, local review has its exact EOF lease
	current.mu.Lock()
	if !current.last.Equal(last) {
		t.Fatal("task-gone heartbeat renewed local-review state")
	}
	current.mu.Unlock()
	cancel()
	clock.advance(time.Minute)
	active.Progress()
	current.mu.Lock()
	if !current.last.Equal(last) {
		t.Fatal("client-gone heartbeat renewed the lease")
	}
	current.mu.Unlock()
}

func TestClientDisconnectCancelsBeforeSourceOpenReturns(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	started := make(chan struct{})
	directory := t.TempDir()
	manager, err := New(t.Context(), Options{Directory: func(string) (string, error) { return directory, nil }, Open: func(ctx context.Context, _, _ string) (Source, error) {
		close(started)
		<-ctx.Done()
		return source, nil // ownership still belongs to the cancelled caller
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	beginDone := make(chan error, 1)
	go func() {
		_, err := manager.Begin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}, 1<<20)
		beginDone <- err
	}()
	<-started
	manager.CloseOwner("outer-owner")
	if err := <-beginDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("late Open result leaked its actual resource")
	}
}
