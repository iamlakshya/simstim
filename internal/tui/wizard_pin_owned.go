package tui

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// pinOwnerBackend keeps the stable Backend interface usable by older fakes.
// A backend advertises ownership only when it can both open and save a stage.
type pinOwnerBackend interface {
	StagePinOwned(context.Context, string) (context.Context, func(), error)
	UpdateHostsContext(context.Context, client.HostChange) (error, error)
}

type wizardPin struct {
	name  string
	ctx   context.Context
	abort func()
	once  sync.Once
}

func (p *wizardPin) close() {
	if p != nil {
		p.once.Do(p.abort)
	}
}

// wizOp coordinates staging with Esc without holding mu across a pin lock.
type wizOp struct {
	mu         sync.Mutex
	stopped    bool
	staged     bool
	prior      bool
	priorOwned *wizardPin
	scope      *wizardPin
	done       chan struct{}
}

// finish joins only a worker that has begun. A command that was dropped or
// invoked after this finalizer cannot open a stage and never holds up exit.
func (o *wizOp) finish() {
	o.mu.Lock()
	o.stopped = true
	done := o.done
	o.mu.Unlock()
	if done != nil {
		<-done
	}
	o.mu.Lock()
	scope := o.scope
	o.mu.Unlock()
	scope.close()
}

func (o *wizOp) stage(ctx context.Context, be Backend, name string) (context.Context, bool, error) {
	o.mu.Lock()
	if o.stopped || o.done != nil {
		o.mu.Unlock()
		return ctx, false, nil
	}
	done := make(chan struct{})
	o.done = done
	o.mu.Unlock()
	defer close(done)
	if o.priorOwned != nil {
		o.priorOwned.close()
	}
	if owner, ok := be.(pinOwnerBackend); ok {
		return o.stageOwned(ctx, owner, name)
	}
	if err := be.StagePin(ctx, name); err != nil {
		o.mu.Lock()
		stopped := o.stopped
		o.mu.Unlock()
		if stopped && o.prior {
			be.AbortPin(name)
		}
		return ctx, false, err
	}
	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		be.AbortPin(name)
		return ctx, false, nil
	}
	o.staged = true
	o.mu.Unlock()
	return ctx, true, nil
}

func (o *wizOp) stageOwned(ctx context.Context, be pinOwnerBackend, name string) (context.Context, bool, error) {
	bound, abort, err := be.StagePinOwned(ctx, name)
	if abort == nil {
		abort = func() {}
	}
	scope := &wizardPin{name: name, ctx: bound, abort: abort}
	if err != nil {
		scope.close()
		return ctx, false, err
	}
	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		scope.close()
		return ctx, false, nil
	}
	o.staged, o.scope = true, scope
	o.mu.Unlock()
	return bound, true, nil
}

func (w *wizard) cancelOperation() {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}

// Each step has its own deadline. Keep the bound stage values after the
// preceding step ends, while linking cancellation to the launcher's context.
func (w *wizard) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if w.pinScope == nil {
		return ctx, cancel
	}
	return wizardPinContext{Context: ctx, values: context.WithoutCancel(w.pinScope.ctx)}, cancel
}

type wizardPinContext struct {
	context.Context
	values context.Context
}

func (c wizardPinContext) Value(key any) any {
	if value := c.values.Value(key); value != nil {
		return value
	}
	return c.Context.Value(key)
}

func (w *wizard) retireOwnedPin(m *Model) tea.Cmd {
	scope := w.pinScope
	m.conns.onClose(scope.close)
	return func() tea.Msg {
		scope.close()
		return nil
	}
}

func saveWizardHost(ctx context.Context, be Backend, c client.HostChange) (warn, err error) {
	if owner, ok := be.(interface {
		UpdateHostsContext(context.Context, client.HostChange) (error, error)
	}); ok {
		return owner.UpdateHostsContext(ctx, c)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return be.UpdateHosts(c)
}
