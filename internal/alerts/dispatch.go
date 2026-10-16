package alerts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Clock tells the time and runs a function later. Tests pass a fake one.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f in its own goroutine after d, unless stop is called first.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// Options configure a Dispatcher. Each zero field takes its default.
type Options struct {
	Path      string        // the alerts file: paths.AlertsConfig()
	Host      string        // the host's name in events: the hostname
	Env       []string      // the environment of command destinations: this process's
	Dir       string        // the working folder of command destinations: the home folder
	Logger    *slog.Logger  // where problems are logged: nowhere
	Clock     Clock         // the real clock
	Limiter   Limiter       // the host-wide limit: a FileLimiter at paths.AlertsRate()
	Log       *DeliveryLog  // the delivery log at paths.AlertsLog()
	HTTP      *http.Client  // NewHTTPClient()
	RetryWait time.Duration // how long to wait before the one retry: 2 s
}

func (o *Options) fill() {
	if o.Path == "" {
		o.Path = paths.AlertsConfig()
	}
	if o.Host == "" {
		o.Host, _ = os.Hostname()
	}
	if o.Env == nil {
		o.Env = os.Environ()
	}
	if o.Dir == "" {
		o.Dir, _ = os.UserHomeDir()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Limiter == nil || o.Log == nil {
		_ = os.MkdirAll(paths.StateDir(), 0o700)
	}
	if o.Limiter == nil {
		o.Limiter = NewFileLimiter(paths.AlertsRate())
	}
	if o.Log == nil {
		o.Log = &DeliveryLog{Path: paths.AlertsLog(), Lock: paths.AlertsRate()}
	}
	if o.HTTP == nil {
		o.HTTP = NewHTTPClient()
	}
	if o.RetryWait == 0 {
		o.RetryWait = retryWait
	}
}

func (o *Options) sinks(set *Settings) []sink {
	e := sinkEnv{env: o.Env, dir: o.Dir, client: o.HTTP}
	out := make([]sink, len(set.Destinations))
	for i, d := range set.Destinations {
		out[i] = newSink(d, e)
	}
	return out
}

// Queue sizes and the per-session limit.
const (
	inQueue    = 64          // events waiting to be looked at
	workQueue  = 16          // events waiting to go out
	sessionGap = time.Minute // one event of a kind per session in this time, except an end
)

// Dispatcher sends one session's events to the destinations in the alerts
// file. Its methods never block the caller: events wait in small queues, and
// an event that finds its queue full is dropped.
type Dispatcher struct {
	o    Options
	in   chan Event
	work chan job
	quit chan struct{}
	done chan struct{} // closed when the last delivery has ended

	gen   atomic.Uint64 // counts the session's state changes
	typed atomic.Int64  // when a person last typed, in Unix nanoseconds
	muted atomic.Bool

	// Only the loop goroutine touches these.
	set    *Settings
	sinks  []sink
	stamp  fileStamp
	loaded bool

	mu      sync.Mutex
	closed  bool
	sent    map[string]time.Time // when each kind last went out
	pending func() bool          // stops the waiting needs_input
}

type job struct {
	ev    Event
	set   *Settings
	sinks []sink
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func (s fileStamp) same(t fileStamp) bool { return s.mod.Equal(t.mod) && s.size == t.size }

// New starts a dispatcher. Close stops it.
func New(o Options) *Dispatcher {
	o.fill()
	d := &Dispatcher{o: o, in: make(chan Event, inQueue), work: make(chan job, workQueue),
		quit: make(chan struct{}), done: make(chan struct{}), sent: map[string]time.Time{}}
	go d.loop()
	go d.worker()
	return d
}

// Notify queues an event. It never blocks.
func (d *Dispatcher) Notify(ev Event) {
	if d.muted.Load() {
		return
	}
	ev.at = d.o.Clock.Now()
	ev.gen = d.gen.Load()
	select {
	case d.in <- ev:
	default:
		d.o.Logger.Warn("alert dropped: too many are waiting", "event", ev.Event)
	}
}

// StateChanged tells the dispatcher that the session's state changed, so a
// needs_input still in its delay is dropped.
func (d *Dispatcher) StateChanged() { d.gen.Add(1) }

// Typed tells the dispatcher that a person typed into the session.
func (d *Dispatcher) Typed() { d.typed.Store(d.o.Clock.Now().UnixNano()) }

// TypedAt returns when a person last typed into the session, or the zero
// time if nobody has.
func (d *Dispatcher) TypedAt() time.Time {
	if n := d.typed.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// Close drops every new event, and waits up to wait for deliveries under way.
func (d *Dispatcher) Close(wait time.Duration) {
	if d.muted.Swap(true) {
		return
	}
	close(d.quit)
	d.mu.Lock()
	d.closed = true
	if d.pending != nil {
		d.pending()
	}
	close(d.work)
	d.mu.Unlock()
	select {
	case <-d.done:
	case <-time.After(wait):
	}
}

func (d *Dispatcher) loop() {
	for {
		select {
		case ev := <-d.in:
			d.handle(ev)
		case <-d.quit:
			return
		}
	}
}

// handle decides what to do with one event: drop it, hold it for the delay,
// or send it.
func (d *Dispatcher) handle(ev Event) {
	set, sinks := d.settings()
	if set == nil || !set.wants(ev.Event) || (ev.Event == TurnDone && ev.Turn < set.MinTurn) {
		return
	}
	if ev.Event != NeedsInput || set.Delay <= 0 {
		d.fire(ev, set, sinks)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if d.pending != nil {
		d.pending() // a newer needs_input replaces the waiting one
	}
	d.pending = d.o.Clock.AfterFunc(set.Delay, func() { d.fire(ev, set, sinks) })
}

// fire applies the noise rules, then queues the event for delivery.
func (d *Dispatcher) fire(ev Event, set *Settings, sinks []sink) {
	if d.muted.Load() {
		return
	}
	if why := d.stale(ev, set); why != "" {
		d.skip(ev, why)
		return
	}
	if why := d.queue(ev, set, sinks); why != "" {
		d.skip(ev, why)
	}
}

// queue applies the limits, then queues ev for delivery. It says why ev
// did not go out, or nothing when it went out or the dispatcher is closed.
// The session's minute for a kind starts only when an event of the kind
// goes out, so an event that the host's limit or a full queue drops does
// not hold back the next one. It holds d.mu throughout, so two events of
// one kind cannot both pass the check.
//
// A session's end is outside the session's minute (decision 0026): the
// minute never holds an end back, and an end does not start it. Each run
// ends once, and restore and resume start a new run in a new holder, so
// each end is new. The host's limit still counts an end.
func (d *Dispatcher) queue(ev Event, set *Settings, sinks []sink) string {
	now := d.o.Clock.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ""
	}
	end := ev.ends()
	if last, ok := d.sent[ev.Event]; ok && !end && now.Sub(last) < sessionGap {
		return "this session sent one " + ev.Event + " in the last minute"
	}
	if !d.o.Limiter.Allow(now) {
		return fmt.Sprintf("the host sent %d alerts in the last minute", HostLimit)
	}
	select {
	case d.work <- job{ev: prepare(ev, d.o.Host, set), set: set, sinks: sinks}:
		if !end {
			d.sent[ev.Event] = now
		}
		return ""
	default:
		return "too many alerts are waiting to go out"
	}
}

// stale says why a needs_input that waited for its delay should not go out.
func (d *Dispatcher) stale(ev Event, set *Settings) string {
	if ev.Event != NeedsInput {
		return ""
	}
	if d.gen.Load() != ev.gen {
		return "the session moved on during the delay"
	}
	if set.QuietWhenTyping && d.typed.Load() > ev.at.UnixNano() {
		return "someone typed into the session during the delay"
	}
	return ""
}

func (d *Dispatcher) worker() {
	defer close(d.done)
	for j := range d.work {
		deliverAll(&d.o, j.ev, j.set, j.sinks, false)
	}
}

// settings returns the alerts file's settings, read again when the file
// changed. It returns nil when alerts are off.
func (d *Dispatcher) settings() (*Settings, []sink) {
	st, err := os.Stat(d.o.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && d.loaded {
			d.o.Logger.Warn("alerts are off: cannot read the alerts file", "error", err)
		}
		d.set, d.sinks, d.loaded = nil, nil, false
		return nil, nil
	}
	stamp := fileStamp{mod: st.ModTime(), size: st.Size()}
	if d.loaded && stamp.same(d.stamp) {
		return d.set, d.sinks
	}
	d.stamp, d.loaded = stamp, true
	set, err := Load(d.o.Path)
	if err != nil || set == nil {
		d.set, d.sinks = nil, nil
		if err != nil {
			d.o.Logger.Warn("alerts are off: the alerts file is not valid", "error", err)
			_ = d.o.Log.Append(api.AlertLogEntry{Time: d.o.Clock.Now().UTC(), Event: "config", Error: err.Error()})
		}
		return nil, nil
	}
	d.set, d.sinks = set, d.o.sinks(set)
	return d.set, d.sinks
}

// skip records an event that was not sent, and why.
func (d *Dispatcher) skip(ev Event, why string) {
	d.o.Logger.Info("alert not sent", "event", ev.Event, "why", why)
	_ = d.o.Log.Append(api.AlertLogEntry{Time: d.o.Clock.Now().UTC(), Event: ev.Event, Session: ev.Session.Name, ID: ev.Session.ID, Skipped: why})
}

// prepare fills in what destinations see: the version, host, time and summary.
// It drops the detail unless the config turns it on.
func prepare(ev Event, host string, set *Settings) Event {
	ev.V = Version
	ev.Host = host
	if ev.Time == "" {
		ev.Time = ev.at.UTC().Format(time.RFC3339)
	}
	if !set.Detail {
		ev.Detail = ""
	}
	ev.Summary = summary(ev)
	return ev
}

// deliverAll sends ev to each destination that takes it (to every one when
// all is true), all at once, and logs each result.
func deliverAll(o *Options, ev Event, set *Settings, sinks []sink, all bool) []api.AlertResult {
	results := make([]*api.AlertResult, len(set.Destinations))
	var wg sync.WaitGroup
	for i, dst := range set.Destinations {
		if !all && !set.takes(i, ev.Event) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
			out, err := deliver(ctx, sinks[i], ev, o.RetryWait)
			cancel()
			r := &api.AlertResult{Type: dst.Type, Target: target(dst), OK: err == nil, MS: time.Since(start).Milliseconds()}
			if err != nil {
				r.Error = err.Error()
				o.Logger.Warn("alert not delivered", "event", ev.Event, "type", r.Type, "target", r.Target, "error", r.Error)
			}
			results[i] = r
			_ = o.Log.Append(api.AlertLogEntry{Time: time.Now().UTC(), Event: ev.Event, Session: ev.Session.Name, ID: ev.Session.ID,
				Type: r.Type, Target: r.Target, OK: r.OK, Error: r.Error, MS: r.MS, Output: out})
		}()
	}
	wg.Wait()
	var out []api.AlertResult
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// SendNow sends ev to every destination in the alerts file at once, whatever
// their events, and ignores the delay and the limits. The test command uses it.
func SendNow(o Options, ev Event) ([]api.AlertResult, error) {
	o.fill()
	set, err := Load(o.Path)
	if err != nil {
		return nil, err
	}
	hint := brand.Name + " host alerts set <host> --ntfy https://ntfy.sh/<your-topic>"
	if set == nil {
		return nil, &Error{Msg: "alerts are off on this host: there is no alerts file", Hint: hint}
	}
	if len(set.Destinations) == 0 {
		return nil, &Error{Msg: "the alerts file names no destination", Hint: hint}
	}
	ev.at = o.Clock.Now()
	ev = prepare(ev, o.Host, set)
	return deliverAll(&o, ev, set, o.sinks(set), true), nil
}
