package tui

import (
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// guideCreate owns one invocation, including a command that has not started.
// Its result survives a program dropping the ordinary createdMsg delivery.
type guideCreate struct {
	mu        sync.Mutex
	started   bool
	stopped   bool
	completed bool
	msg       createdMsg
	done      chan struct{}
	once      sync.Once
}

func (r *guideCreate) start() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	r.started = true
	return true
}

func (r *guideCreate) end() { r.once.Do(func() { close(r.done) }) }

func (r *guideCreate) complete(msg createdMsg) {
	r.mu.Lock()
	r.msg, r.completed = msg, true
	r.mu.Unlock()
}

func (r *guideCreate) result() (createdMsg, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msg, r.completed
}

func (r *guideCreate) requestStop() bool {
	r.mu.Lock()
	r.stopped = true
	started := r.started
	r.mu.Unlock()
	if !started {
		r.end()
	}
	return started
}

func (r *guideCreate) stopAndWait() {
	r.requestStop()
	<-r.done
}

// stopGuide joins its provider work before another modal or Close takes over.
// A successful provider already saved its host, so no successful machine is
// deleted and no new preparation is started just to dispose the old modal.
func (m *Model) stopGuide(g *guide) {
	g.halt()
	if r := g.creation; r != nil {
		if msg, ok := r.result(); ok && msg.err == nil {
			m.rememberCreatedHost(msg.host)
		}
		g.creation = nil
	}
}

func (g *guide) interrupted() {
	g.halt()
	g.creation, g.note = nil, ""
	for i := range g.checks {
		if g.checks[i].state == ckRunning {
			g.checks[i].state = ckFailed
		}
	}
	g.err = &client.UserError{Msg: "the host operation stopped before it finished", Hint: "Press Enter to try again.", Code: "cancelled", Kind: client.KindCancelled}
}

// resumeGuide runs only after pauseBg joined the old program's commands.
// Existing result handling retains its owner, request and phase guards.
func (m *Model) resumeGuide() tea.Cmd {
	g, ok := m.modal.(*guide)
	if !ok {
		return nil
	}
	if r := g.creation; r != nil {
		if msg, complete := r.result(); complete {
			return g.onCreated(m, msg)
		}
	}
	if g.run {
		g.interrupted()
	}
	return nil
}

// disposeModal stops its ownership before a late start installs a question.
// Prepared wizard connections remain tracked until their cleanup command or
// the next pauseBg closes them.
func (m *Model) disposeModal() tea.Cmd {
	switch d := m.modal.(type) {
	case *wizard:
		d.stop()
		return m.dropConn(d.takeConn())
	case *guide:
		m.stopGuide(d)
	}
	return nil
}
