package pullexport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestRealReadProgressRenewsBeyondIdleOnlyWithinExactScope(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, clock, ticks := managerFixture(t, source)
	args := api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}
	run, err := manager.PrepareBeginRequest(t.Context(), "outer-owner", "primary2", args, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := manager.ReadRequest(t.Context(), "outer-owner", api.PullReadArgs{ID: args.ID, Token: args.Token}, 0)
	if err != nil {
		t.Fatal(err)
	}
	progress := api.PullOutputProgress{Event: api.PullOutputProgressEvent, ID: args.ID, Token: args.Token, Epoch: 0, Request: 0, Next: chunk.Next}
	for range 8 {
		clock.advance(time.Minute)
		if !manager.Touch(t.Context(), "outer-owner", progress) {
			t.Fatal("actual current progress failed after two-minute elapsed duration")
		}
		ticks <- clock.read()
	}
	for _, mutate := range []func(*api.PullOutputProgress){
		func(p *api.PullOutputProgress) { p.ID = "another2" }, func(p *api.PullOutputProgress) { p.Token = strings.Repeat("b", 26) },
		func(p *api.PullOutputProgress) { p.Epoch = 1 }, func(p *api.PullOutputProgress) { p.Request = 1 }, func(p *api.PullOutputProgress) { p.Next-- },
	} {
		wrong := progress
		mutate(&wrong)
		if manager.Touch(t.Context(), "outer-owner", wrong) {
			t.Fatal("a stale or different scope renewed the lease", wrong)
		}
	}
	if manager.Touch(t.Context(), "other-owner", progress) {
		t.Fatal("a different outer owner renewed this export")
	}
	next, err := manager.PrepareReadRequest(t.Context(), "outer-owner", api.PullReadArgs{ID: args.ID, Token: args.Token, Offset: descriptor.Size}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Touch(t.Context(), "outer-owner", progress) {
		t.Fatal("queued old progress crossed synchronous new read admission")
	}
	newProgress := progress
	newProgress.Request = 1
	if manager.Touch(t.Context(), "outer-owner", newProgress) {
		t.Fatal("a not-yet-produced reply claimed transport progress")
	}
	if _, err = next(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !manager.Touch(t.Context(), "outer-owner", newProgress) {
		t.Fatal("actual new EOF reply scope was not current")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if manager.Touch(cancelled, "outer-owner", newProgress) {
		t.Fatal("caller cancellation lost to progress")
	}
	if _, err = manager.Finish(t.Context(), "outer-owner", api.PulledArgs{ID: args.ID, Token: args.Token, Abort: true}); err != nil {
		t.Fatal(err)
	}
	if manager.Touch(t.Context(), "outer-owner", newProgress) {
		t.Fatal("progress resurrected an ended export")
	}
}

func TestOutputProgressCannotCrossFullRetryEpoch(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, _, _ := managerFixture(t, source)
	args := api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}
	run, err := manager.PrepareBeginRequest(t.Context(), "outer-owner", "primary2", args, 1<<20, 17)
	if err != nil {
		t.Fatal(err)
	}
	first, err := run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ReadRequest(t.Context(), "outer-owner", api.PullReadArgs{ID: args.ID, Token: args.Token}, 18); err != nil {
		t.Fatal(err)
	}
	old := api.PullOutputProgress{Event: api.PullOutputProgressEvent, ID: args.ID, Token: args.Token, Epoch: 17, Request: 18, Next: first.Size}
	if !manager.Touch(t.Context(), "outer-owner", old) {
		t.Fatal("old scope positive control failed")
	}
	retry, err := manager.PrepareBeginRequest(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: args.ID, Token: strings.Repeat("b", 26), Previous: args.Token, Full: true}, 1<<20, 19)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Touch(t.Context(), "outer-owner", old) {
		t.Fatal("old token/epoch renewed full preparation")
	}
	full, err := retry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ReadRequest(t.Context(), "outer-owner", api.PullReadArgs{ID: full.ID, Token: full.Token}, 20); err != nil {
		t.Fatal(err)
	}
	old.Token = full.Token
	old.Next = full.Size
	if manager.Touch(t.Context(), "outer-owner", old) {
		t.Fatal("new token donated an old epoch/request")
	}
}
