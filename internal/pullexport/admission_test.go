package pullexport

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestFreshProtectedCapCanOnlyNarrowBeforeCapture(t *testing.T) {
	for _, cap := range []int64{0, -1, 4} {
		source := &exportFixture{closed: make(chan struct{}), cap: &cap}
		manager, _, _ := managerFixture(t, source)
		if _, err := manager.Begin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}, 1<<20); err == nil {
			t.Fatal("fresh invalid or tighter cap was ignored", cap)
		}
		select {
		case <-source.closed:
		default:
			t.Fatal("fresh cap refusal leaked actual ownership")
		}
	}
}

func TestOrderedReservationAbortBeforeCaptureNeverOpensSource(t *testing.T) {
	directory := t.TempDir()
	opened := 0
	manager, err := New(t.Context(), Options{Directory: func(string) (string, error) { return directory, nil }, Open: func(context.Context, string, string) (Source, error) {
		opened++
		t.Fatal("cancelled reservation launched a source")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	args := api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}
	run, err := manager.PrepareBegin(t.Context(), "outer-owner", "primary2", args, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Finish(t.Context(), "outer-owner", api.PulledArgs{ID: args.ID, Token: args.Token, Abort: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t.Context()); err == nil || opened != 0 {
		t.Fatal("late begin revived cancelled admission", err, opened)
	}
}

func TestOriginalCaptureClosureCannotRunFullRetryEpoch(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, _, _ := managerFixture(t, source)
	firstArgs := api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}
	old, err := manager.PrepareBegin(t.Context(), "outer-owner", "primary2", firstArgs, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first, err := old(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: first.ID, Token: first.Token}); err != nil {
		t.Fatal(err)
	}
	retry, err := manager.PrepareBegin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: first.ID, Token: strings.Repeat("b", 26), Previous: first.Token, Full: true}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old(t.Context()); err == nil {
		t.Fatal("old run captured a later full-retry epoch")
	}
	if _, err = retry(t.Context()); err != nil {
		t.Fatal("actual full-retry owner was displaced", err)
	}
}

func TestFullRetryCannotRetargetOriginalMember(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, _, _ := managerFixture(t, source)
	first := beginFixture(t, manager, strings.Repeat("a", 26))
	if _, err := manager.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: first.ID, Token: first.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareBegin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: "another2", Token: strings.Repeat("b", 26), Previous: first.Token, Full: true}, 1<<20); err == nil {
		t.Fatal("full retry changed original ID within the same primary")
	}
	if _, err := manager.Finish(t.Context(), "outer-owner", api.PulledArgs{ID: first.ID, Token: first.Token, Abort: true}); err != nil {
		t.Fatal(err)
	}
}
