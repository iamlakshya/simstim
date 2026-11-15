package alerts

// Stream-only event kinds do not select an alert destination.
const (
	StateChanged    = "state_changed"
	Ended           = "ended"
	EgressHeld      = "egress_held"
	ClassifierAllow = "classifier_allow"
)

// StreamKinds is the event vocabulary of events.watch V1.
var StreamKinds = []string{StateChanged, NeedsInput, TurnDone, Ended, EgressHeld, ClassifierAllow, SandboxReady, SandboxStopped, SandboxError, HealthDegraded, HealthRecovered}

// IsStreamKind checks an event name without accepting future event meanings.
func IsStreamKind(kind string) bool {
	for _, known := range StreamKinds {
		if known == kind {
			return true
		}
	}
	return false
}

const eventHistory = 128
const eventWatchers = 8

type eventBuffer struct {
	history  []Event
	watchers map[*EventWatch]bool
}

// EventWatch owns one bounded view of the dispatcher's event stream. Snapshot
// and future events share one admission point, so no event falls between them.
// Slow consumers are closed with an error instead of silently losing events.
type EventWatch struct {
	Snapshot []Event
	Events   <-chan Event
	owner    *Dispatcher
	queue    chan Event
	failure  error
	closed   bool
}

// WatchEvents subscribes without a reader goroutine, listening socket or poll.
// Follow=false returns the same bounded snapshot and an already closed stream.
func (d *Dispatcher) WatchEvents(follow bool) *EventWatch {
	d.mu.Lock()
	defer d.mu.Unlock()
	watch := &EventWatch{owner: d, queue: make(chan Event, eventHistory)}
	watch.Events = watch.queue
	for _, ev := range d.events.history {
		watch.Snapshot = append(watch.Snapshot, copyStreamEvent(ev))
	}
	if !follow || d.closed {
		watch.closed = true
		close(watch.queue)
		return watch
	}
	if len(d.events.watchers) >= eventWatchers {
		watch.failure = &Error{Msg: "too many event streams are watching this session", Hint: "Close another event stream, then try again."}
		watch.closed = true
		close(watch.queue)
		return watch
	}
	if d.events.watchers == nil {
		d.events.watchers = make(map[*EventWatch]bool)
	}
	d.events.watchers[watch] = true
	return watch
}

// Close releases only this subscription. It never ends its session.
func (w *EventWatch) Close() {
	w.owner.mu.Lock()
	defer w.owner.mu.Unlock()
	if !w.closed {
		delete(w.owner.events.watchers, w)
		w.closed = true
		close(w.queue)
	}
}

// Err reports why a stream ended, or nil after normal closure.
func (w *EventWatch) Err() error {
	w.owner.mu.Lock()
	defer w.owner.mu.Unlock()
	return w.failure
}

func copyStreamEvent(ev Event) Event {
	if ev.ExitCode != nil {
		code := *ev.ExitCode
		ev.ExitCode = &code
	}
	return ev
}

func (d *Dispatcher) publishEvent(ev Event) {
	switch ev.Event {
	case Exited, Failed:
		ev.Event = Ended
	}
	if !IsStreamKind(ev.Event) {
		return
	}
	// Streaming is independent of alert destinations and their ten-second
	// needs_input delay. Detail is private and stays off on this channel.
	ev = prepare(ev, d.o.Host, &Settings{})
	ev = copyStreamEvent(ev)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if len(d.events.history) == eventHistory {
		copy(d.events.history, d.events.history[1:])
		d.events.history = d.events.history[:eventHistory-1]
	}
	d.events.history = append(d.events.history, ev)
	for watch := range d.events.watchers {
		select {
		case watch.queue <- copyStreamEvent(ev):
		default:
			watch.failure = &Error{Msg: "the event stream did not keep up with this session", Hint: "Run events again to read its recent events."}
			watch.closed = true
			delete(d.events.watchers, watch)
			close(watch.queue)
		}
	}
}

func (d *Dispatcher) closeEventsLocked() {
	for watch := range d.events.watchers {
		watch.closed = true
		close(watch.queue)
		delete(d.events.watchers, watch)
	}
}
