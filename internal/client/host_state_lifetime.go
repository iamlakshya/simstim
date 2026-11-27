package client

import (
	"context"
	"sync"
)

type hostStateParticipantKind uint8

const (
	hostOrdinaryParticipant hostStateParticipantKind = iota + 1
	hostTransportParticipant
)

// One lifetime belongs to one actual OP descriptor. Metadata mutation excludes
// ordinary users, while terminal retirement and physical close drain BOTH
// ordinary and non-authorising transport users before any descriptor closure.
type hostStateLifetime struct {
	mu         sync.Mutex
	closed     bool
	retiring   bool
	readers    int
	transports int
	changed    chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	workCtx    context.Context
	stopWork   context.CancelFunc
}

func newHostStateLifetime() *hostStateLifetime {
	ctx, cancel := context.WithCancel(context.Background())
	work, stop := context.WithCancel(ctx)
	return &hostStateLifetime{changed: make(chan struct{}), ctx: ctx, cancel: cancel, workCtx: work, stopWork: stop}
}

func (held *heldLock) stateLifetime() *hostStateLifetime {
	held.stateOnce.Do(func() { held.state = newHostStateLifetime() })
	if held.gone.Load() {
		held.state.beginClose()
	}
	return held.state
}

func (l *hostStateLifetime) notifyLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *hostStateLifetime) register(ctx context.Context, kind hostStateParticipantKind) (context.Context, func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, nil, hostStateRemovalError("the host state operation ended")
		}
		if !l.retiring {
			switch kind {
			case hostOrdinaryParticipant:
				l.readers++
			case hostTransportParticipant:
				l.transports++
			default:
				l.mu.Unlock()
				return nil, nil, hostStateRemovalError("the operation participant has no supported role")
			}
			// Capture the cancellation generation AT admission. A cancelled
			// retirement cannot revive an old admitted user by replacing it.
			work := l.workCtx
			l.mu.Unlock()
			return work, sync.OnceFunc(func() {
				l.mu.Lock()
				if kind == hostTransportParticipant {
					l.transports--
				} else {
					l.readers--
				}
				l.notifyLocked()
				l.mu.Unlock()
			}), nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-changed:
		}
	}
}

func (l *hostStateLifetime) enter(ctx context.Context) (func(), error) {
	_, leave, err := l.register(ctx, hostOrdinaryParticipant)
	return leave, err
}

func participantContext(parent, work context.Context, leave func()) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(work, cancel)
	return ctx, sync.OnceFunc(func() { stop(); cancel(); leave() })
}

func (l *hostStateLifetime) enterTransaction(ctx context.Context) (context.Context, func(), error) {
	work, leave, err := l.register(ctx, hostOrdinaryParticipant)
	if err != nil {
		return ctx, nil, err
	}
	bound, release := participantContext(ctx, work, leave)
	return bound, release, nil
}

// enterTransport is non-authorising. Its startup child is cancelled by terminal
// quiescence/physical close, and its count does not self-block real H4 mutation.
func (l *hostStateLifetime) enterTransport(ctx context.Context) (context.Context, func(), error) {
	work, leave, err := l.register(ctx, hostTransportParticipant)
	if err != nil {
		return ctx, nil, err
	}
	bound, release := participantContext(ctx, work, leave)
	return bound, release, nil
}

func hostMetadataBusyError() error {
	return &UserError{Msg: "the host has an active publication or local state operation",
		Hint: "Wait until that operation finishes, then retry the metadata change.", Code: "host_busy"}
}

// mutate never waits for a caller's own ordinary participant. Reuse of an
// existing actual exclusive scope is checked by holdHostMutationContext.
func (l *hostStateLifetime) mutate(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, hostStateRemovalError("the host state operation ended")
	}
	if l.retiring || l.readers != 0 {
		return nil, hostMetadataBusyError()
	}
	l.retiring = true
	l.notifyLocked()
	return l.endExclusive(false), nil
}

// retire is terminal quiescence, BEFORE membership. No counter is forced to
// zero. Cancellation preserves old cancelled contexts and authorises no I/O.
func (l *hostStateLifetime) retire(ctx context.Context) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, hostStateRemovalError("the host state operation ended")
		}
		if !l.retiring {
			l.retiring = true
			l.stopWork()
			l.notifyLocked()
			l.mu.Unlock()
			break
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
	release := l.endExclusive(true)
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			release()
			return nil, hostStateRemovalError("the host state operation ended")
		}
		if l.readers == 0 && l.transports == 0 {
			l.mu.Unlock()
			return release, nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (l *hostStateLifetime) endExclusive(renewWork bool) func() {
	return sync.OnceFunc(func() {
		l.mu.Lock()
		if renewWork && !l.closed {
			l.workCtx, l.stopWork = context.WithCancel(l.ctx)
		}
		l.retiring = false
		l.notifyLocked()
		l.mu.Unlock()
	})
}

func (l *hostStateLifetime) beginClose() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.cancel()
	l.stopWork()
	l.notifyLocked()
}

func (l *hostStateLifetime) drain() {
	for {
		l.mu.Lock()
		if l.readers == 0 && l.transports == 0 && !l.retiring {
			l.mu.Unlock()
			return
		}
		changed := l.changed
		l.mu.Unlock()
		<-changed
	}
}

func (l *hostStateLifetime) transactionContext(parent context.Context) (context.Context, func()) {
	l.mu.Lock()
	work := l.workCtx
	l.mu.Unlock()
	return participantContext(parent, work, func() {})
}

// A verified finaliser runs after ordinary/transport work was cancelled. Its
// own context follows physical owner closure and parent cancellation instead.
func (l *hostStateLifetime) retirementContext(parent context.Context) (context.Context, func()) {
	return participantContext(parent, l.ctx, func() {})
}
