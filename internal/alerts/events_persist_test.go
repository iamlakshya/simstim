package alerts

import (
	"errors"
	"testing"
)

func TestEventPersistenceReceivesRedactedIdentityBeforeLivePublication(t *testing.T) {
	d := eventDispatcher(t)
	var persisted Event
	d.o.PublishEvent = func(event Event) error { persisted = event; return nil }
	w := d.WatchEvents(true)
	defer w.Close()
	d.Notify(Event{Event: NeedsInput, Session: Session{ID: "abcd2345"}, Detail: "private question", DetailFrom: FromGuest, Claimed: true})
	live := <-w.Events
	if persisted.Stream == "" || persisted.Sequence != 1 || live.Stream != persisted.Stream || live.Sequence != persisted.Sequence || persisted.Detail != "" || persisted.DetailFrom != "" || !persisted.Claimed {
		t.Fatalf("native persistence/live boundary changed: stored=%+v live=%+v", persisted, live)
	}
}

func TestEventPersistenceFailureCannotPublishOrHideAStreamGap(t *testing.T) {
	d := eventDispatcher(t)
	want := errors.New("native append failed")
	d.o.PublishEvent = func(Event) error { return want }
	w := d.WatchEvents(true)
	d.Notify(Event{Event: SandboxError, Session: Session{ID: "abcd2345"}})
	if _, open := <-w.Events; open {
		t.Fatal("an unpersisted event was presented as a native history record")
	}
	if !errors.Is(w.Err(), want) {
		t.Fatalf("persistence gap hidden: %v", w.Err())
	}
	next := d.WatchEvents(false)
	if len(next.Snapshot) != 0 || !errors.Is(next.Err(), want) {
		t.Fatalf("a later snapshot silently filled a persistence gap: %+v %v", next.Snapshot, next.Err())
	}
}
