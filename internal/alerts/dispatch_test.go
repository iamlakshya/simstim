package alerts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// fakeClock moves only when a test says so. Its timers run in the goroutine
// that calls Advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

// Advance moves the clock on and runs the timers that came due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			due = append(due, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// waitTimer waits until one timer is waiting.
func (c *fakeClock) waitTimer(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		k := 0
		for _, x := range c.timers {
			if !x.stopped && !x.fired {
				k++
			}
		}
		c.mu.Unlock()
		if k == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no waiting timer")
}

// rig is a dispatcher with a fake clock, writing to a test server.
type rig struct {
	t     *testing.T
	d     *Dispatcher
	clock *fakeClock
	path  string
	log   *DeliveryLog
	got   chan Event
}

func newRig(t *testing.T, cfg string, limiter Limiter) *rig {
	t.Helper()
	r := &rig{t: t, clock: newFakeClock(), got: make(chan Event, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var ev Event
		b, _ := io.ReadAll(req.Body)
		if json.Unmarshal(b, &ev) == nil {
			r.got <- ev
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	r.path = filepath.Join(dir, "alerts.toml")
	r.write(strings.ReplaceAll(cfg, "URL", srv.URL))
	r.log = &DeliveryLog{Path: filepath.Join(dir, "alerts.log"), Lock: filepath.Join(dir, "alerts.rate")}
	if limiter == nil {
		limiter = &FileLimiter{Path: filepath.Join(dir, "alerts.rate"), Max: 1000, Window: time.Minute}
	}
	r.d = New(Options{Path: r.path, Host: "box", Clock: r.clock, Limiter: limiter, Log: r.log, RetryWait: time.Millisecond})
	t.Cleanup(func() { r.d.Close(time.Second) })
	return r
}

func (r *rig) write(cfg string) {
	r.t.Helper()
	if err := os.WriteFile(r.path, []byte(cfg), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) event(kind string) Event {
	return Event{Event: kind, Session: Session{ID: "abc123", Name: "auth-fix", Agent: "claude", Dir: "/src"}, Detail: "Do you want to proceed?"}
}

// expect waits for one delivered event of kind.
func (r *rig) expect(kind string) Event {
	r.t.Helper()
	select {
	case ev := <-r.got:
		if ev.Event != kind {
			r.t.Fatalf("got %s, want %s", ev.Event, kind)
		}
		return ev
	case <-time.After(5 * time.Second):
		r.t.Fatalf("no %s was delivered", kind)
	}
	return Event{}
}

// none checks that nothing was delivered.
func (r *rig) none() {
	r.t.Helper()
	select {
	case ev := <-r.got:
		r.t.Fatalf("%s was delivered", ev.Event)
	case <-time.After(100 * time.Millisecond):
	}
}

// skipped waits for a log line that says why an event was not sent.
func (r *rig) skipped(why string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := r.log.Tail(100)
		for _, e := range entries {
			if strings.Contains(e.Skipped, why) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("no log line says %q", why)
}

const hookCfg = "events = [\"needs_input\", \"failed\", \"turn_done\", \"oom\", \"exited\"]\n[[destination]]\ntype = \"webhook\"\nurl = \"URL\"\n"

func TestDelayThenSend(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(NeedsInput))
	r.clock.waitTimer(t)
	r.clock.Advance(9 * time.Second)
	r.none()
	r.clock.Advance(time.Second)
	ev := r.expect(NeedsInput)
	if ev.V != 1 || ev.Host != "box" || ev.Summary != "auth-fix on box needs you" || ev.Time != "2026-10-15T10:00:00Z" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Detail != "" {
		t.Error("detail was sent with detail off")
	}
	entries := r.logged(1)
	if len(entries) != 1 || !entries[0].OK || entries[0].Type != "webhook" || entries[0].Session != "auth-fix" {
		t.Errorf("log = %+v", entries)
	}
}

func TestDelayDroppedWhenStateChanges(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(NeedsInput))
	r.clock.waitTimer(t)
	r.d.StateChanged()
	r.clock.Advance(10 * time.Second)
	r.skipped("moved on")
	r.none()
}

// The holder reads when a person last typed, for its memory warning.
func TestTypedAt(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	if at := r.d.TypedAt(); !at.IsZero() {
		t.Fatalf("TypedAt before any typing = %v", at)
	}
	r.clock.Advance(time.Second)
	r.d.Typed()
	if at, want := r.d.TypedAt(), r.clock.Now(); !at.Equal(want) {
		t.Errorf("TypedAt = %v, want %v", at, want)
	}
}

func TestDelayDroppedWhenTyping(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Typed() // typing before the question does not count
	r.clock.Advance(time.Second)
	r.d.Notify(r.event(NeedsInput))
	r.clock.waitTimer(t)
	r.clock.Advance(3 * time.Second)
	r.d.Typed()
	r.clock.Advance(7 * time.Second)
	r.skipped("typed")
	r.none()

	// With quiet_when_typing off, typing does not stop it.
	r.write("quiet_when_typing = false\ndetail = true\n" + strings.SplitN(r.read(), "\n", 2)[1])
	r.clock.Advance(time.Minute)
	r.d.Notify(r.event(NeedsInput))
	r.clock.waitTimer(t)
	r.d.Typed()
	r.clock.Advance(10 * time.Second)
	if ev := r.expect(NeedsInput); ev.Detail != "Do you want to proceed?" {
		t.Errorf("detail = %q with detail on", ev.Detail)
	}
}

// logged waits until the log has at least n entries, and returns them.
func (r *rig) logged(n int) []api.AlertLogEntry {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := r.log.Tail(100)
		if len(entries) >= n {
			return entries
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("the log has %d entries, want %d", len(entries), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *rig) read() string {
	b, _ := os.ReadFile(r.path)
	return string(b)
}

// end is a session's end, as the holder raises it: with the program's exit
// code, and the signal that stopped it, if any.
func (r *rig) end(kind string, code int, signal string) Event {
	ev := r.event(kind)
	ev.ExitCode, ev.Signal = &code, signal
	return ev
}

// notHeld checks that the per-session limit held nothing back.
func (r *rig) notHeld() {
	r.t.Helper()
	entries, _ := r.log.Tail(100)
	for _, e := range entries {
		if strings.Contains(e.Skipped, "this session sent one") {
			r.t.Errorf("the log says %q", e.Skipped)
		}
	}
}

// A session sends one event of a kind a minute. The oom of a process that
// the program started is not the session's end, so a second one waits.
func TestPerSessionLimit(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(OOM))
	r.expect(OOM)
	r.clock.Advance(30 * time.Second)
	r.d.Notify(r.event(OOM))
	r.skipped("this session sent one oom in the last minute")
	long := r.event(TurnDone)
	long.Turn = 2 * time.Minute
	r.d.Notify(long) // another kind is not limited
	r.expect(TurnDone)
	r.clock.Advance(31 * time.Second)
	r.d.Notify(r.event(OOM))
	r.expect(OOM)
}

// The cap stops a process that the program started, then the program
// itself within the minute. The second oom is the session's end, which the
// per-session limit never holds back (decision 0026).
func TestEndNotHeldBack(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(OOM))
	if ev := r.expect(OOM); ev.ExitCode != nil {
		t.Errorf("a child's oom has exit code %d", *ev.ExitCode)
	}
	r.clock.Advance(10 * time.Second)
	r.d.Notify(r.end(OOM, 137, "killed"))
	if ev := r.expect(OOM); ev.ExitCode == nil || *ev.ExitCode != 137 || ev.Signal != "killed" {
		t.Errorf("the program's oom = %+v", ev)
	}
	r.notHeld()
}

// Each run of a session ends once, but two runs can end within a minute.
// Today a restore or a resume starts a new holder, whose dispatcher has no
// minute yet. Both ends go out under one dispatcher too, also with code 0.
func TestEndsOfTwoRuns(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.end(Failed, 1, ""))
	r.expect(Failed)
	r.clock.Advance(20 * time.Second)
	r.d.Notify(r.end(Failed, 2, ""))
	if ev := r.expect(Failed); ev.ExitCode == nil || *ev.ExitCode != 2 {
		t.Errorf("the second run's end = %+v", ev)
	}
	r.d.Notify(r.end(Exited, 0, ""))
	r.expect(Exited)
	r.clock.Advance(20 * time.Second)
	r.d.Notify(r.end(Exited, 0, ""))
	r.expect(Exited)
	r.notHeld()
}

// An end does not start the minute: a child's oom in a later run still goes
// out within a minute of the last run's oom. That child's oom starts the
// minute, so a second one within it is dropped.
func TestEndStartsNoMinute(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.end(OOM, 137, "killed"))
	r.expect(OOM)
	r.clock.Advance(10 * time.Second)
	r.d.Notify(r.event(OOM))
	if ev := r.expect(OOM); ev.ExitCode != nil {
		t.Errorf("a child's oom = %+v", ev)
	}
	r.notHeld()
	r.clock.Advance(10 * time.Second)
	r.d.Notify(r.event(OOM))
	r.skipped("this session sent one oom in the last minute")
	r.none()
}

// The host's limit counts a session's end like any other event.
func TestHostLimitHoldsForEnds(t *testing.T) {
	lim := &FileLimiter{Path: filepath.Join(t.TempDir(), "rate"), Max: 1, Window: time.Minute}
	r := newRig(t, hookCfg, lim)
	r.d.Notify(r.event(OOM))
	r.expect(OOM)
	r.d.Notify(r.end(OOM, 137, "killed"))
	r.skipped("the host sent")
	r.none()
	r.notHeld()
}

// The memory warning goes out at once, also while a person types, and its
// limit is its own: a needs_input in the same minute still goes out.
func TestMemoryNotHeldBack(t *testing.T) {
	r := newRig(t, "events = [\"needs_input\", \"memory\"]\ndelay = \"0s\"\n[[destination]]\ntype = \"webhook\"\nurl = \"URL\"\n", nil)
	r.d.Typed()
	ev := r.event(Memory)
	ev.Usage = "2.7 of 2.9 GiB"
	r.d.Notify(ev)
	if got := r.expect(Memory); got.Summary != "auth-fix on box is close to its memory cap (2.7 of 2.9 GiB)" {
		t.Errorf("summary = %q", got.Summary)
	}
	r.d.Notify(r.event(NeedsInput))
	r.expect(NeedsInput)
	r.clock.Advance(30 * time.Second)
	r.d.Notify(r.event(Memory))
	r.skipped("one memory in the last minute")
	r.none()
}

func TestHostLimit(t *testing.T) {
	dir := t.TempDir()
	lim := &FileLimiter{Path: filepath.Join(dir, "rate"), Max: 2, Window: time.Minute}
	r := newRig(t, hookCfg, lim)
	for _, k := range []string{Failed, OOM, Exited} {
		r.d.Notify(r.event(k))
	}
	r.expect(Failed)
	r.expect(OOM)
	r.skipped("the host sent")
	r.none()
}

// refuseFirst is a host at its limit for the first event only.
type refuseFirst struct {
	mu   sync.Mutex
	seen bool
}

func (l *refuseFirst) Allow(time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	was := l.seen
	l.seen = true
	return was
}

// An event that the host's limit drops was not sent, so it starts no
// minute for the session: the next event of its kind goes out.
func TestHostLimitStartsNoMinute(t *testing.T) {
	r := newRig(t, hookCfg, &refuseFirst{})
	r.d.Notify(r.event(Failed))
	r.skipped("the host sent")
	r.clock.Advance(10 * time.Second)
	r.d.Notify(r.event(Failed))
	r.expect(Failed)
	entries, _ := r.log.Tail(100)
	for _, e := range entries {
		if strings.Contains(e.Skipped, "this session sent one") {
			t.Errorf("the log says %q, but no failed went out before", e.Skipped)
		}
	}
}

func TestEventsAndMinTurn(t *testing.T) {
	r := newRig(t, "events = [\"turn_done\"]\nmin_turn = \"60s\"\n[[destination]]\ntype = \"webhook\"\nurl = \"URL\"\n", nil)
	r.d.Notify(r.event(Failed)) // not in events
	short := r.event(TurnDone)
	short.Turn = 59 * time.Second
	r.d.Notify(short)
	long := r.event(TurnDone)
	long.Turn = 61 * time.Second
	r.d.Notify(long)
	// Events are handled in order, so the long turn arriving first means the
	// others were dropped.
	if ev := r.expect(TurnDone); ev.Summary != "auth-fix on box finished a turn (1m1s)" {
		t.Errorf("summary = %q", ev.Summary)
	}
	r.none()
}

func TestReloadAndOff(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(Failed))
	r.expect(Failed)
	r.logged(1)

	// A change takes effect at the next event; a broken file turns alerts off
	// and says why in the log.
	r.write("events = [\"nope\"]\n")
	r.d.Notify(r.event(OOM))
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := r.log.Tail(10)
		if e := entries[len(entries)-1]; e.Event == "config" && strings.Contains(e.Error, "unknown event") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the broken file was not logged")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.none()

	if err := os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	r.d.Notify(r.event(Exited))
	r.none()
}

func TestCloseMutes(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(NeedsInput))
	r.clock.waitTimer(t)
	r.d.Close(time.Second)
	r.clock.Advance(time.Minute)
	r.d.Notify(r.event(Failed))
	r.none()
}

func TestSendNow(t *testing.T) {
	r := newRig(t, "events = [\"failed\"]\n[[destination]]\ntype = \"webhook\"\nurl = \"URL\"\nevents = [\"oom\"]\n[[destination]]\ntype = \"webhook\"\nurl = \"https://127.0.0.1:1/secret-path\"\n", nil)
	ev, err := TestEvent(NeedsInput, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	res, err := SendNow(Options{Path: r.path, Host: "box", Log: r.log, RetryWait: time.Millisecond}, ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].OK || res[1].OK || res[1].Target != "https://127.0.0.1:1/…" {
		t.Errorf("results = %+v", res)
	}
	got := r.expect(NeedsInput)
	if got.Summary != "Test: alerts-test on box needs you" {
		t.Errorf("summary = %q", got.Summary)
	}
	raw, _ := os.ReadFile(r.log.Path)
	if strings.Contains(string(raw), "secret-path") {
		t.Errorf("the log shows a secret: %s", raw)
	}

	if _, err := SendNow(Options{Path: filepath.Join(t.TempDir(), "none.toml"), Log: r.log}, ev); err == nil || !strings.Contains(err.Error(), "off") {
		t.Errorf("SendNow with no file = %v", err)
	}
	if _, err := TestEvent("nope", ""); err == nil {
		t.Error("TestEvent took an unknown kind")
	}
}

func TestLogRecordsResults(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(Failed))
	r.expect(Failed)
	entries := r.logged(1)
	if len(entries) != 1 || entries[0].Event != Failed || entries[0].ID != "abc123" || !strings.HasPrefix(entries[0].Target, "http://127.0.0.1:") {
		t.Errorf("log = %+v", entries)
	}
}
