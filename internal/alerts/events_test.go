package alerts

import (
	"path/filepath"
	"testing"
	"time"
)

func eventDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	d := New(Options{Host: "box", Path: filepath.Join(t.TempDir(), "missing-alerts.toml"), Clock: newFakeClock()})
	t.Cleanup(func() { d.Close(time.Second) })
	return d
}

func TestEventStreamNeedsNoDestinationOrAlertDelay(t *testing.T) {
	d := eventDispatcher(t)
	w := d.WatchEvents(true)
	defer w.Close()
	d.Notify(Event{Event: NeedsInput, Session: Session{ID: "abcd2345", Name: "job"}, Detail: "private question"})
	select {
	case ev := <-w.Events:
		if ev.V != 1 || ev.Event != NeedsInput || ev.Host != "box" || ev.Detail != "" || ev.Summary != "job on box needs you" || ev.Time == "" {
			t.Fatalf("event=%+v", ev)
		}
	default:
		t.Fatal("needs_input waited for alert configuration or its delay")
	}
}

func TestEventStreamSnapshotAndSubscriptionHaveNoGap(t *testing.T) {
	d := eventDispatcher(t)
	d.Notify(Event{Event: StateChanged, Reason: "working"})
	w := d.WatchEvents(true)
	defer w.Close()
	d.Notify(Event{Event: TurnDone})
	if len(w.Snapshot) != 1 || w.Snapshot[0].Event != StateChanged {
		t.Fatalf("snapshot=%v", w.Snapshot)
	}
	select {
	case ev := <-w.Events:
		if ev.Event != TurnDone {
			t.Fatalf("future event=%+v", ev)
		}
	default:
		t.Fatal("the subscription missed its next event")
	}
	select {
	case ev := <-w.Events:
		t.Fatalf("duplicate event=%+v", ev)
	default:
	}
}

func TestEventStreamSlowSubscriberClosesWithErrorAndHistoryStaysBounded(t *testing.T) {
	d := eventDispatcher(t)
	w := d.WatchEvents(true)
	for i := 0; i <= eventHistory; i++ {
		d.Notify(Event{Event: NeedsInput})
	}
	if w.Err() == nil {
		t.Fatal("a slow event consumer silently lost events")
	}
	n := 0
	for range w.Events {
		n++
	}
	if n != eventHistory {
		t.Fatalf("queue=%d", n)
	}
	snapshot := d.WatchEvents(false)
	defer snapshot.Close()
	if len(snapshot.Snapshot) != eventHistory {
		t.Fatalf("history=%d", len(snapshot.Snapshot))
	}
	if _, open := <-snapshot.Events; open {
		t.Fatal("snapshot-only watch stayed open")
	}
}

func TestEventStreamOwnsExitCodeAndClosesWithDispatcher(t *testing.T) {
	d := eventDispatcher(t)
	code := 7
	d.Notify(Event{Event: Failed, ExitCode: &code})
	code = 99
	w := d.WatchEvents(true)
	if len(w.Snapshot) != 1 || w.Snapshot[0].Event != Ended || *w.Snapshot[0].ExitCode != 7 {
		t.Fatalf("owned event=%+v", w.Snapshot)
	}
	*w.Snapshot[0].ExitCode = 42
	other := d.WatchEvents(false)
	if *other.Snapshot[0].ExitCode != 7 {
		t.Fatal("snapshot mutated dispatcher history")
	}
	d.Close(time.Second)
	if _, open := <-w.Events; open {
		t.Fatal("dispatcher close left a subscriber alive")
	}
	w.Close()
}
