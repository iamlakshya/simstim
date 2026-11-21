package checkpoint

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestActorRootKeepsStrictOwnershipAndSeparateNativeTopologyV3(t *testing.T) {
	f := newFixture(t)
	actor, err := OpenActorRootV3(t.Context(), f.vm, f.disk, func(ctx context.Context) error { return ctx.Err() })
	if actor != nil {
		t.Cleanup(func() { _ = actor.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := actor.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	legacy, err := f.store.entries.Stat(".")
	actual, actorErr := actor.entries.Stat(".")
	if err != nil || actorErr != nil || os.SameFile(legacy, actual) {
		t.Fatal("actor storage reinterpreted legacy checkpoint roots", err, actorErr)
	}
	if generic, err := OpenOwned(t.Context(), f.vm, f.vm, f.disk, func(ctx context.Context) error { return ctx.Err() }); err == nil {
		_ = generic.Close()
		t.Fatal("generic checkpoint storage accepted the actor topology")
	}
	if err := f.vm.Rename(diskName, "old-disk"); err != nil {
		t.Fatal(err)
	}
	if err := actor.Check(t.Context()); err == nil {
		t.Fatal("an actor root retained authority after its original named disk moved")
	}
}

func TestActorListPageUsesExclusiveCheckedTimestampCursorV3(t *testing.T) {
	f := newFixture(t)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := range 3 {
		f.write(t, "actual rooted checkpoint bytes")
		if _, err := f.store.Create(t.Context(), at.Add(time.Duration(index)*time.Minute), metadata("fixed"), 3); err != nil {
			t.Fatal(err)
		}
	}
	after := ""
	for index := 2; index >= 0; index-- {
		entry, err := f.store.ListPageV3(t.Context(), after)
		if err != nil || entry == nil || !entry.Time.Equal(at.Add(time.Duration(index)*time.Minute)) {
			t.Fatal("the actual rooted page changed its exclusive cursor", err)
		}
		after = entry.Time.Format(TimeLayout)
	}
	if entry, err := f.store.ListPageV3(t.Context(), after); err != nil || entry != nil {
		t.Fatal("the rooted page did not terminate", err)
	}
	if _, err := f.store.ListPageV3(t.Context(), "../another"); err == nil {
		t.Fatal("a caller path became a native checkpoint cursor")
	}
}
