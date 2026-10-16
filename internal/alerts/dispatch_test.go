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

func TestPerSessionLimit(t *testing.T) {
	r := newRig(t, hookCfg, nil)
	r.d.Notify(r.event(Failed))
	r.expect(Failed)
	r.clock.Advance(30 * time.Second)
	r.d.Notify(r.event(Failed))
	r.skipped("in the last minute")
	r.d.Notify(r.event(OOM)) // another kind is not limited
	r.expect(OOM)
	r.clock.Advance(31 * time.Second)
	r.d.Notify(r.event(Failed))
	r.expect(Failed)
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
