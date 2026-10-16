package host

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/ring"
)

// stepClock moves on an hour at each reading. A session sends one event of
// a kind a minute, and this limit must not hide a repeat from a test.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Hour)
	return c.now
}

func (c *stepClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// stillClock never moves, so the per-session limit's minute never passes.
type stillClock struct{ at time.Time }

func (c stillClock) Now() time.Time { return c.at }

func (stillClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

type allowAll struct{}

func (allowAll) Allow(time.Time) bool { return true }

// oomRig is a holder in a fake unit whose out-of-memory count the test
// sets. When the file done appears, its agent exits with the code in it.
// Its alerts reach the test.
type oomRig struct {
	t      *testing.T
	h      *Holder
	events string // the unit's memory.events
	done   string
	got    chan alerts.Event
}

func newOOMRig(t *testing.T) *oomRig {
	t.Helper()
	return newOOMRigAt(t, &stepClock{now: time.Now()})
}

// newOOMRigAt is newOOMRig with the alert dispatcher's clock.
func newOOMRigAt(t *testing.T, clock alerts.Clock) *oomRig {
	t.Helper()
	alertHome(t)
	r := &oomRig{t: t, done: filepath.Join(t.TempDir(), "done"), got: make(chan alerts.Event, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		var ev alerts.Event
		b, _ := io.ReadAll(req.Body)
		if json.Unmarshal(b, &ev) == nil {
			r.got <- ev
		}
	}))
	t.Cleanup(srv.Close)
	cfg := fmt.Sprintf("events = [%q, %q, %q, %q, %q]\ndelay = \"0s\"\n[[destination]]\ntype = \"webhook\"\nurl = %q\n",
		alerts.Started, alerts.NeedsInput, alerts.Failed, alerts.OOM, alerts.Exited, srv.URL)
	writeTestFile(t, paths.AlertsConfig(), cfg)

	id := NewID()
	unit := "/user.slice/" + brand.Slice + "/" + UnitName(id)
	root := t.TempDir()
	old := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = old })
	fakeProc(t, map[string]string{"self/cgroup": "0::" + unit + "\n"})
	r.events = filepath.Join(root, unit, "memory.events")
	r.kills(0)

	m := &Manifest{Schema: 1, ID: id, Name: "hog", Agent: AgentInfo{Kind: "custom", Argv: []string{"/bin/sh", "-c", `while [ ! -s "$0" ]; do sleep 0.02; done; exit "$(cat "$0")"`, r.done}},
		Cwd: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Term: TermInfo{Cols: 80, Rows: 24}}
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	h := &Holder{id: id, m: m, log: slog.New(slog.DiscardHandler), ring: ring.New(1 << 16),
		clients: map[*hclient]struct{}{}, readDone: make(chan struct{})}
	close(h.readDone) // nothing reads the PTY here
	h.al.d = alerts.New(alerts.Options{Clock: clock, Limiter: allowAll{}, Env: m.Env, Dir: m.Cwd})
	t.Cleanup(func() { h.al.d.Close(time.Second) })
	if err := h.start(); err != nil {
		t.Fatal(err)
	}
	pid := h.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL); h.ptmx.Close() })
	r.h = h
	return r
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// kills sets the unit's out-of-memory count, as the kernel does at a kill.
func (r *oomRig) kills(n int) {
	writeTestFile(r.t, r.events, fmt.Sprintf("low 0\nhigh 0\nmax %d\noom %d\noom_kill %d\n", n, n, n))
}

// ticks runs n of the holder's ticks.
func (r *oomRig) ticks(n int) {
	for range n {
		r.h.step()
	}
}

// kill ends the agent with SIGKILL, as the kernel or a person can, and
// lets the holder see the end.
func (r *oomRig) kill() {
	if err := r.h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		r.t.Fatal(err)
	}
	r.h.waitLoop()
}

// killWhile ends the agent with SIGKILL. After the reap, it runs during
// while the holder waits for the agent's last output.
func (r *oomRig) killWhile(during func()) {
	r.h.readDone = make(chan struct{})
	ended := make(chan struct{})
	go func() {
		r.h.waitLoop()
		close(ended)
	}()
	if err := r.h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		r.t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !r.reaped(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			r.t.Fatal("the holder did not reap the agent")
		}
	}
	during()
	close(r.h.readDone)
	<-ended
}

func (r *oomRig) reaped() bool {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	return r.h.reaped
}

// exit makes the agent exit with code, and lets the holder see the end.
func (r *oomRig) exit(code int) {
	writeTestFile(r.t, r.done, strconv.Itoa(code))
	r.h.waitLoop()
}

func (r *oomRig) status() Status {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	return r.h.m.Status
}

// sent returns the kinds of the alerts sent since the last call. A started
// event goes last as a marker: the dispatcher sends in order.
func (r *oomRig) sent() []string {
	r.t.Helper()
	r.h.al.d.Notify(alerts.Event{Event: alerts.Started, Session: alerts.Session{ID: r.h.id}})
	var kinds []string
	for {
		select {
		case ev := <-r.got:
			if ev.Event == alerts.Started {
				return kinds
			}
			kinds = append(kinds, ev.Event)
		case <-time.After(5 * time.Second):
			r.t.Fatalf("the alerts did not arrive; have %q", kinds)
			return nil
		}
	}
}

func (r *oomRig) wantSent(want ...string) {
	r.t.Helper()
	if got := r.sent(); !slices.Equal(got, want) {
		r.t.Errorf("alerts %q, want %q", got, want)
	}
}

// The memory cap stops a process that the agent started: the session is
// marked once, with one oom alert. The agent still runs, so the session
// does not start to need input. A later SIGKILL of the agent that is not
// out of memory ends it as a plain kill.
func TestOOMChildKillThenAgentKill(t *testing.T) {
	r := newOOMRig(t)
	r.kills(1)
	r.ticks(1)
	if st := r.status(); st.OOM {
		t.Fatal("the first tick that saw the kill claimed it: it could be the agent's own")
	}
	r.ticks(3)
	st := r.status()
	if !st.OOM || st.Reason != oomChildReason || st.State == StateNeedsInput || r.h.needInput {
		t.Fatalf("after a child's kill the status is %+v (needInput %v)", st, r.h.needInput)
	}
	r.wantSent(alerts.OOM)

	r.kill()
	r.ticks(2)
	st = r.status()
	if st.OOM || st.State != StateErrored || st.Signal != "killed" || st.Reason != "" {
		t.Fatalf("an agent killed after a child's kill: %+v", st)
	}
	r.wantSent(alerts.Failed)
}

// The memory cap stops the agent itself.
func TestOOMAgentKill(t *testing.T) {
	r := newOOMRig(t)
	r.kills(1)
	r.kill()
	r.ticks(2)
	st := r.status()
	if !st.OOM || st.State != StateErrored || st.Reason != "oom" || st.Signal != "killed" {
		t.Fatalf("the agent's own kill: %+v", st)
	}
	r.wantSent(alerts.OOM)
}

// The kernel counts a kill before the victim dies. A tick that sees the
// count before the agent is reaped only notes it. So the agent's own kill
// is still out of memory, and not a child's.
func TestOOMAgentKillSeenBeforeReap(t *testing.T) {
	r := newOOMRig(t)
	r.kills(1)
	r.ticks(1)
	r.kill()
	r.ticks(2)
	if st := r.status(); !st.OOM || st.State != StateErrored || st.Reason != "oom" {
		t.Fatalf("the agent's own kill, seen by a tick first: %+v", st)
	}
	r.wantSent(alerts.OOM)
}

// A child's kill, then the agent's own a tick later: each counts once.
func TestOOMChildKillThenAgentOOM(t *testing.T) {
	r := newOOMRig(t)
	r.kills(1)
	r.ticks(1)
	r.kills(2)
	r.ticks(1) // claims the first kill, notes the second
	if st := r.status(); !st.OOM || st.Reason != oomChildReason {
		t.Fatalf("the child's kill: %+v", st)
	}
	r.kill()
	if st := r.status(); !st.OOM || st.State != StateErrored || st.Reason != "oom" {
		t.Fatalf("the agent's kill after a child's: %+v", st)
	}
	r.wantSent(alerts.OOM, alerts.OOM)
}

// A child's kill, then the agent's own, with a clock that never moves: the
// minute between them never passes. The agent's oom is the session's end,
// so the per-session limit does not hold it back (decision 0026).
func TestOOMChildKillThenAgentOOMWithinAMinute(t *testing.T) {
	r := newOOMRigAt(t, stillClock{at: time.Now()})
	r.kills(1)
	r.ticks(2)
	r.kills(2)
	r.kill()
	// No marker here: the limit would hold back a second started.
	for i := range 2 {
		select {
		case ev := <-r.got:
			if ev.Event != alerts.OOM || (ev.ExitCode != nil) != (i == 1) {
				t.Fatalf("alert %d: %s, exit code %v", i+1, ev.Event, ev.ExitCode)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%d oom alerts arrived, want 2", i)
		}
	}
}

// After a child's kill, the agent exits by itself, and the session has
// ended well. Later ticks do not mark it again. This is also true when the
// cap stops a process that the agent left behind.
func TestOOMChildKillThenAgentExits(t *testing.T) {
	r := newOOMRig(t)
	r.kills(1)
	r.ticks(2)
	r.wantSent(alerts.OOM)
	r.exit(0)
	r.kills(2)
	r.ticks(3)
	st := r.status()
	if st.OOM || st.State != StateExited || st.Reason != "" || st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("an agent that exited after a child's kill: %+v", st)
	}
	r.wantSent(alerts.Exited)
}

// A person stops the agent with SIGKILL, and a process that the agent left
// behind keeps the terminal open. The cap stops that process while the
// holder waits for the agent's last output. The agent's end is a plain
// kill, not out of memory.
func TestOOMLeftoverKillAfterAgentKill(t *testing.T) {
	r := newOOMRig(t)
	r.killWhile(func() { r.kills(1) })
	r.ticks(2)
	if st := r.status(); st.OOM || st.State != StateErrored || st.Signal != "killed" || st.Reason != "" {
		t.Fatalf("a kill, then a leftover's kill at the cap: %+v", st)
	}
	r.wantSent(alerts.Failed)
}

// The cap stops a process that the agent started, as make's cc1. The agent
// exits with code 2 before a tick claims the kill. The session ends with
// the child's reason and one oom alert, but without the OOM flag: the cap
// did not stop the agent.
func TestOOMChildKillThenQuickExit(t *testing.T) {
	r := newOOMRig(t)
	r.kills(1)
	r.ticks(1) // notes the kill
	r.exit(2)
	r.ticks(2)
	st := r.status()
	if st.OOM || st.State != StateErrored || st.ExitCode == nil || *st.ExitCode != 2 || st.Reason != oomChildReason {
		t.Fatalf("an exit with code 2 just after a child's kill: %+v", st)
	}
	r.wantSent(alerts.OOM, alerts.Failed)
}

// While the agent waits for the user, the reason is its question, also
// after a child's kill. When it works again, the out-of-memory reason is
// back.
func TestOOMReasonGivesWayToQuestion(t *testing.T) {
	h := &Holder{m: &Manifest{Status: Status{State: StateWorking, OOM: true, Reason: oomChildReason}}, lastOut: time.Now()}
	h.needInput, h.notifyMsg = true, "Claude needs your permission to use Bash"
	h.updateStatus()
	if st := h.m.Status; st.State != StateNeedsInput || st.Reason != h.notifyMsg || !st.OOM {
		t.Fatalf("waiting after a child's kill: %+v", st)
	}
	h.needInput, h.notifyMsg = false, ""
	h.updateStatus()
	if st := h.m.Status; st.State != StateWorking || st.Reason != oomChildReason || !st.OOM {
		t.Fatalf("working again: %+v", st)
	}
}
