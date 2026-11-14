package tui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type ownerDeadlineContext struct {
	context.Context
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error
}

func (c *ownerDeadlineContext) Done() <-chan struct{} { return c.done }
func (c *ownerDeadlineContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}
func (c *ownerDeadlineContext) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *ownerDeadlineContext) end(err error) {
	c.once.Do(func() { c.mu.Lock(); c.err = err; c.mu.Unlock(); close(c.done) })
}

type ownerDeadlineLocal struct {
	onboard.Local
	expire func()
}

func (l ownerDeadlineLocal) SetIdle(ctx context.Context, _ client.Host) (string, error) {
	l.expire()
	return "", ctx.Err()
}

type ownerDeadlineBackend struct {
	*handoffBackend
	local onboard.Local
}

func (b ownerDeadlineBackend) Local() onboard.Local { return b.local }

func TestWizardStepDeadlineReportsFailureWhileParentLives(t *testing.T) {
	for _, point := range []string{"dial", "idle"} {
		t.Run(point, func(t *testing.T) {
			m, _, w, be, _ := newHandoffCommandModel(t)
			oldFactory := wizardSetupContext
			var deadline *ownerDeadlineContext
			wizardSetupContext = func(parent context.Context) (context.Context, context.CancelFunc) {
				deadline = &ownerDeadlineContext{Context: parent, done: make(chan struct{})}
				stop := context.AfterFunc(parent, func() { deadline.end(context.Canceled) })
				return deadline, func() { stop(); deadline.end(context.Canceled) }
			}
			t.Cleanup(func() { wizardSetupContext = oldFactory })
			w.host = client.Host{Name: "new", SSH: "new"}
			if point == "dial" {
				be.dialHook = func(context.Context, client.Host) { deadline.end(context.DeadlineExceeded) }
			} else {
				w.made = true
				w.host.Machine = &client.Machine{Provider: onboard.Lima, ID: "owned-test"}
				m.be = ownerDeadlineBackend{handoffBackend: be, local: ownerDeadlineLocal{Local: be.Local(), expire: func() { deadline.end(context.DeadlineExceeded) }}}
			}
			msg, ok := ownerResult(t, w.setup(m)).(setupMsg)
			if !ok || !errors.Is(msg.err, context.DeadlineExceeded) || w.ctx.Err() != nil {
				t.Fatalf("step deadline vanished or cancelled parent: %#v parent=%v", msg, w.ctx.Err())
			}
			c := be.conns[len(be.conns)-1]
			t.Cleanup(c.Close)
			select {
			case <-c.Done():
			default:
				t.Fatal("timed-out setup left its connection open")
			}
			if m.conns.count() != 1 {
				t.Fatal("timed-out setup stayed registered")
			}
			_ = m.onSetup(msg)
			if w.run || w.err == nil || w.checks[0].state != ckFailed {
				t.Fatal("step deadline left a perpetual spinner")
			}
			wizardSetupContext = oldFactory
			be.dialHook = nil
			retry, ok := ownerResult(t, w.key(m, tea.KeyPressMsg{Code: tea.KeyEnter})).(setupMsg)
			if !ok || retry.err != nil || retry.conn == nil {
				t.Fatalf("deadline did not permit retry: %#v", retry)
			}
		})
	}
}
