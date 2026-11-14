package tui

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type progStepper struct {
	t           *testing.T
	pm          tea.Model
	m           *Model
	results     chan tea.Msg
	commands    sync.WaitGroup
	running     atomic.Int64
	timers      []timerMsg
	quits       int
	tierWaiters atomic.Int32
	inputStops  atomic.Int32
	readerStuck atomic.Bool
}

func newProgStepper(t *testing.T, pm tea.Model, m *Model) *progStepper {
	t.Helper()
	ps := &progStepper{t: t, pm: pm, m: m, results: make(chan tea.Msg, 512)}
	m.tierWait = func(wait func() tea.Msg) tea.Msg {
		ps.tierWaiters.Add(1)
		defer ps.tierWaiters.Add(-1)
		return wait()
	}
	if stop := m.stopInput; stop != nil {
		m.stopInput = func() bool {
			stuck := stop()
			ps.readerStuck.Store(stuck)
			ps.inputStops.Add(1)
			return stuck
		}
	}
	m.after = func(d time.Duration, msg tea.Msg) tea.Cmd {
		return func() tea.Msg { return timerMsg{d, msg} }
	}
	return ps
}

func (ps *progStepper) start(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	ps.running.Add(1)
	ps.commands.Add(1)
	go func() {
		defer ps.commands.Done()
		msg := cmd()
		ps.results <- msg
		ps.running.Add(-1)
	}()
}

func (ps *progStepper) stepUntil(cmd tea.Cmd, extraBlocked func() int64) {
	ps.t.Helper()
	ps.start(cmd)
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case msg := <-ps.results:
			ps.deliver(msg)
			continue
		default:
		}
		parked := int64(ps.m.waiters.Load()) + int64(ps.tierWaiters.Load())
		if extraBlocked != nil {
			parked += extraBlocked()
		}
		if ps.running.Load() <= parked {
			time.Sleep(time.Millisecond)
			if len(ps.results) == 0 && ps.running.Load() <= parked {
				return
			}
			continue
		}
		if time.Now().After(deadline) {
			ps.t.Fatalf("commands did not settle: running=%d parked=%d", ps.running.Load(), parked)
		}
		time.Sleep(time.Millisecond)
	}
}

func (ps *progStepper) step(cmd tea.Cmd) {
	ps.stepUntil(cmd, nil)
}

// attachQuit follows asynchronous tier resolution through normal Update.
// A quit is accepted only after its owned reader stopped and the exact
// foreground attach intent, state and remembered session were retained.
func (ps *progStepper) attachQuit(cmd tea.Cmd, id string, extraBlocked func() int64) {
	ps.t.Helper()
	const host = "box"
	quits, stops := ps.quits, ps.inputStops.Load()
	ps.stepUntil(cmd, extraBlocked)
	if ps.quits-quits != 1 {
		ps.t.Fatalf("attach emitted %d Quit messages, want one", ps.quits-quits)
	}
	if ps.inputStops.Load()-stops != 1 || ps.readerStuck.Load() {
		ps.t.Fatal("attach quit before its one owned reader stop completed")
	}
	pa := ps.m.pendingAttach
	if pa == nil || pa.host != host || pa.id != id || pa.opts.Host.Name != host || pa.opts.Session != id || pa.opts.State != ps.m.attach || !pa.opts.Interactive || ps.m.pendingExec != nil || ps.m.jacking != nil {
		ps.t.Fatalf("quit retained the wrong attach owner: %+v", pa)
	}
	if last := ps.m.mem.Last; last == nil || last.Host != host || last.ID != id {
		ps.t.Fatal("quit changed the remembered attach target")
	}
}

// deliver runs on the test's single Update thread, as Bubble Tea does.
func (ps *progStepper) deliver(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil, tea.RawMsg:
	case tea.QuitMsg:
		ps.quits++
	case tea.BatchMsg:
		for _, c := range msg {
			ps.start(c)
		}
	case timerMsg:
		ps.timers = append(ps.timers, msg)
	default:
		_, next := ps.pm.Update(msg)
		ps.start(next)
	}
}

// stepToGate pumps only the messages required to reach the active backend
// operation. Once its gate is entered, no later result is sent to Update.
// The returned join waits only for already-started commands, without delivery.
func (ps *progStepper) stepToGate(cmd tea.Cmd, entered func() bool) <-chan struct{} {
	ps.t.Helper()
	ps.start(cmd)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	joined := func() <-chan struct{} {
		done := make(chan struct{})
		go func() { ps.commands.Wait(); close(done) }()
		return done
	}
	for {
		if entered() {
			return joined()
		}
		select {
		case msg := <-ps.results:
			if entered() {
				return joined()
			} // deliberately leave this result undelivered
			ps.deliver(msg)
		case <-poll.C:
			// The gated operation is blocked, so it need not produce a message.
		case <-deadline.C:
			if entered() {
				return joined()
			}
			ps.t.Fatal("the request did not reach its gate")
		}
	}
}
