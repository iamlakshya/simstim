package client

import (
	"context"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// HostAttemptPlanV1 owns one selection and at most one active New attempt.
// Names and placement needs remain metadata; each attempt gets a fresh request.
type HostAttemptPlanV1 struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	selection     *HostSelectionV1
	args          NewArgs
	need          PlacementNeed
	names         []string
	next          int
	active        *HostAttemptV1
	automatic     *AutoPlacementPlanV1
	wake          AutoPlacementOptionsV1
	autoAttempts  int
	closed        bool
	stop          func() bool
	selectionStop func() bool
	closeJob      *hostAttemptCleanupV1
}

// HostAttemptV1 owns the actual request and every connection allocated by its
// Dial, including failed hello/install retries. It never owns a remote session.
type HostAttemptV1 struct {
	mu               sync.Mutex
	plan             *HostAttemptPlanV1
	target           string
	host             Host
	conn             *Conn
	request          *GrantRequest
	ctx              context.Context
	cancel           context.CancelFunc
	running          chan struct{}
	failure          *hostAttemptFailureV1
	dial             *ownedNewDialV1
	ready            bool
	closing          bool
	closed           bool
	started          bool
	newCalled        bool
	cleanup          *hostAttemptCleanupV1
	candidate        *autoPlacementCandidate
	placement        *ownedPlacementRecord
	placementBinding *preparedPlacementBindingV1
	retirement       *newSecretPlan
	cachedRefusal    *settledNewRefusalV1
}

// NewHostAttemptPlanV1 takes ownership of selection on success. args contains
// original intent only. Prepared inputs, answers, values and runtime selections
// belong to one attempt and cannot be donated to the next host.
func NewHostAttemptPlanV1(ctx context.Context, selection *HostSelectionV1, args NewArgs, need PlacementNeed, options ...AutoPlacementOptionsV1) (*HostAttemptPlanV1, error) {
	if ctx == nil || selection == nil || grantRequestOf(ctx) != nil || len(options) > 1 {
		return nil, grantOperationError()
	}
	if err := selection.Check(ctx); err != nil {
		return nil, err
	}
	if !freshHostAttemptArgsV1(args) {
		return nil, grantOperationError()
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &HostAttemptPlanV1{ctx: pctx, cancel: cancel, selection: selection,
		args: cloneHostAttemptArgsV1(args), need: cloneHostAttemptNeedV1(need)}
	if len(options) == 1 {
		p.wake = options[0]
	}
	var prepareErr error
	if selection.Automatic() {
		// Supplied by the separate real automatic planner. It owns gathering,
		// ranking, wake consent and concrete candidate observations.
		automatic, err := PrepareAutoPlacementV1(pctx, selection, args, need, p.wake)
		if err != nil && automatic == nil {
			cancel()
			return nil, err
		}
		// Partial automatic plans can own failed facts connections. Return
		// this same owner on error so Close can still join them.
		p.automatic, prepareErr = automatic, err
	} else {
		p.names = selection.Names()
		if len(p.names) == 0 {
			cancel()
			return nil, grantOperationError()
		}
	}
	selection.mu.Lock()
	original := selection.ctx
	selection.mu.Unlock()
	if original == nil {
		cancel()
		return nil, grantOperationError()
	}
	p.mu.Lock()
	p.selectionStop = context.AfterFunc(original, cancel)
	p.stop = context.AfterFunc(pctx, func() { _ = p.Close() })
	p.mu.Unlock()
	if err := pctx.Err(); err != nil {
		_ = p.Close()
		return p, err
	}
	if prepareErr != nil {
		cancel()
		return p, prepareErr
	}
	return p, nil
}

// Next returns an owned attempt even when its actual Dial fails. Pass that
// attempt and the exact returned error to Retry before requesting another.
// Named lists have no automatic-placement three-host limit.
func (p *HostAttemptPlanV1) Next(ctx context.Context, interactive bool) (*HostAttemptV1, error) {
	if p == nil || ctx == nil {
		return nil, grantOperationError()
	}
	p.mu.Lock()
	if err := p.admitNextLockedV1(ctx); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.automatic != nil {
		p.mu.Unlock()
		return p.nextAutomaticV1(ctx, interactive)
	}
	if p.next >= len(p.names) {
		p.mu.Unlock()
		return nil, hostAttemptExhaustedV1()
	}
	name := p.names[p.next]
	p.mu.Unlock()
	return p.newAttemptForTargetV1(ctx, name, nil, interactive)
}

func (p *HostAttemptPlanV1) admitNextLockedV1(ctx context.Context) error {
	if p.ctx == nil || p.cancel == nil || p.selection == nil || p.closed || p.active != nil {
		return grantOperationError()
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

// This is the common native owner used by both named and genuine automatic
// candidates. ctx supplies only a narrower budget and, optionally, WithoutWake.
func (p *HostAttemptPlanV1) newAttemptForTargetV1(ctx context.Context, name string, candidate *autoPlacementCandidate, interactive bool) (*HostAttemptV1, error) {
	p.mu.Lock()
	if err := p.admitNextLockedV1(ctx); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.automatic == nil && (candidate != nil || p.next >= len(p.names) || p.names[p.next] != name) ||
		p.automatic != nil && (candidate == nil || candidate.host.Name != name) {
		p.mu.Unlock()
		return nil, grantOperationError()
	}
	owned, cancel := context.WithCancel(p.ctx)
	// Allocate R before binding, host lookup or Dial. No previous R is reused.
	a := &HostAttemptV1{plan: p, target: name, ctx: owned, cancel: cancel,
		request: NewGrantRequest(p.need.Untrusted, p.args.SSHAgent),
		running: make(chan struct{}), candidate: candidate}
	// H5 adds this private field to R. Only the actual allocator fixes it;
	// no public argument, error, context boolean or callback can replace it.
	a.request.hostAttempt = a
	p.active = a
	p.next++
	if p.automatic != nil {
		p.autoAttempts++
	}
	p.mu.Unlock()
	defer a.endOperationV1()

	var bound context.Context
	var err error
	if candidate == nil {
		bound, err = p.selection.BindNewRequest(owned, a.request, name)
	} else {
		// The separate concrete gathering stage closes positive intake and
		// the wire to metadata reads. It does not weaken the strict binder.
		bound, err = p.bindAutomaticAttemptV1(owned, a.request, candidate)
	}
	if err != nil {
		return a, a.recordFailureV1(err, hostAttemptLocalV1)
	}
	a.mu.Lock()
	a.ctx = bound
	a.mu.Unlock()
	return a.dialTargetV1(ctx, interactive)
}

func (a *HostAttemptV1) Host() Host {
	if a == nil {
		return Host{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneObservationHost(a.host)
}

func (a *HostAttemptV1) Conn() *Conn {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn
}

func (a *HostAttemptV1) Request() *GrantRequest {
	if a == nil {
		return nil
	}
	return a.request
}

// Context returns the original bound request context. NewSession accepts a
// caller context only to narrow cancellation/deadline; it takes no new values.
func (a *HostAttemptV1) Context() context.Context {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}

func (a *HostAttemptV1) Args() NewArgs {
	if a == nil || a.plan == nil {
		return NewArgs{}
	}
	return cloneHostAttemptArgsV1(a.plan.args)
}

func freshHostAttemptArgsV1(a NewArgs) bool {
	return a.ID == "" && a.HostLabel == "" && a.Placement == nil && a.Runtime == nil && len(a.VMNeedV1) == 0 &&
		a.Inputs == nil && a.Environment == nil && a.SecretDelivery == nil && a.RepositoryPolicy == nil &&
		a.GrantHash == "" && a.GrantAnswers == nil && a.Secrets == nil && a.ProtectedTrustedSecretValues == nil &&
		a.SetupTrust == nil && a.SetupHashes == nil && len(a.Layers) == 0
}

func cloneHostAttemptArgsV1(a NewArgs) NewArgs {
	a.Argv, a.Mounts, a.Include = slices.Clone(a.Argv), slices.Clone(a.Mounts), slices.Clone(a.Include)
	a.Layers = api.CloneLayers(a.Layers)
	return a
}

func cloneHostAttemptNeedV1(need PlacementNeed) PlacementNeed {
	need.Features, need.RuntimeDigests = slices.Clone(need.Features), slices.Clone(need.RuntimeDigests)
	return need
}
