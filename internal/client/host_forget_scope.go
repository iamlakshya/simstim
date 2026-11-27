package client

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
)

type hostForgetMembershipKey struct{}
type stateAuthorityKey struct{}
type hostForgetMembership struct {
	mu         sync.Mutex
	workers    sync.WaitGroup
	name       string
	original   Host
	generation string
	active     atomic.Bool
	terminal   *hostTerminalPlan
	cancel     context.CancelFunc
}

func stateContextAuthority(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if scope, _ := ctx.Value(hostMembershipContextKey{}).(*hostMembershipContext); scope != nil {
		if err := scope.check(); err != nil {
			return err
		}
	}
	if inspection, _ := ctx.Value(hostTerminalInspectionKey{}).(*hostTerminalInspection); inspection != nil {
		if err := inspection.check(ctx); err != nil {
			return err
		}
	}
	verified, _ := ctx.Value(hostForgetMembershipKey{}).(*hostForgetMembership)
	if observation, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); observation != nil && (verified == nil || !verified.active.Load()) {
		if err := observation.checkOwner(ctx); err != nil {
			return err
		}
	}
	if mutation, _ := ctx.Value(hostMutationContextKey{}).(*hostMutationScope); mutation != nil {
		if err := mutation.check(); err != nil {
			return err
		}
	}
	if check, _ := ctx.Value(stateAuthorityKey{}).(func() error); check != nil {
		return check()
	}
	return nil
}

// WithHostForgetCommittedContext keeps membership protected around local grant
// tombstones and cleanup. Use verified for that work. Membership writes, prompts,
// remote operations and root scripts are forbidden inside this callback.
func WithHostForgetCommittedContext(ctx context.Context, original Host, work func(verified context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHostName(original.Name) {
		return badHostNameError(original.Name)
	}
	if active, _ := ctx.Value(hostForgetMembershipKey{}).(*hostForgetMembership); active != nil {
		if !active.active.Load() || active.name != original.Name || !SameEntry(active.original, original) {
			return hostStateRemovalError("the verified host membership scope ended")
		}
		release, err := active.enter()
		if err != nil {
			return err
		}
		defer release()
		if err := stateContextAuthority(ctx); err != nil {
			return err
		}
		return work(ctx)
	}
	held, _ := ctx.Value(heldKey{original.Name}).(*heldLock)
	if held == nil || held.gone.Load() {
		return hostStateRemovalError("the original host operation lease is not held")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return err
	}
	lifetime := held.stateLifetime()
	endRetirement, err := lifetime.retire(ctx)
	if err != nil {
		return err
	}
	defer endRetirement()
	ctx, stopOperation := lifetime.retirementContext(ctx)
	defer stopOperation()
	ctx, unlock, err := acquireHostMembershipContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	budget, cancel := context.WithTimeout(ctx, hostsWait)
	defer cancel()
	current, err := openHostMembershipSnapshot(budget)
	if err != nil {
		return err
	}
	defer current.close()
	hosts, err := LoadHosts()
	if stopped := budget.Err(); stopped != nil {
		return stopped
	}
	if err != nil {
		return err
	}
	if slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == original.Name }) {
		return hostStateRemovalError("a saved entry still uses the original host name")
	}
	intent, _ := ctx.Value(hostForgetIntentKey{}).(*hostForgetIntent)
	if intent != nil {
		if !SameEntry(intent.original, original) {
			return hostStateRemovalError("the removal intent changed original host")
		}
	}
	if !hasHostForgetWitness(budget, original, intent, current.generation) {
		if intent == nil {
			return hostStateRemovalError("the original forget has no committed witness or durable receipt")
		}
		prepared, err := readHostForgetReceipt(budget, intent)
		if err != nil {
			return err
		}
		if !validHostForgetPair(prepared) || prepared.TargetGeneration != current.generation {
			return hostStateRemovalError("the removal receipt belongs to an older host membership generation")
		}
	}
	return runHostForgetCommittedScope(budget, original, held, current, work)
}

func runHostForgetCommittedScope(budget context.Context, original Host, held *heldLock, current *hostMembershipSnapshot, work func(context.Context) error) error {
	// Reuse the private immutable original already captured by the actual
	// intent or same-OP CAS witness; do not add another whole-Host copy.
	frozen, err := committedHostForgetOriginal(budget, original, held, current.generation)
	if err != nil {
		return err
	}
	scoped, cancel := context.WithCancel(budget)
	scope := &hostForgetMembership{name: original.Name, original: frozen, generation: current.generation, cancel: cancel}
	scope.active.Store(true)
	defer scope.stop()
	verified := context.WithValue(scoped, hostForgetMembershipKey{}, scope)
	verified = context.WithValue(verified, stateAuthorityKey{}, func() error {
		if !scope.active.Load() || held.gone.Load() {
			return hostStateRemovalError("the verified original removal scope ended")
		}
		if err := current.check(budget); err != nil {
			return err
		}
		if !scope.active.Load() || held.gone.Load() {
			return hostStateRemovalError("the verified original removal scope ended")
		}
		return nil
	})
	callbackErr := work(verified)
	// Nested transactions and their revoked views must finish before any
	// native domain closure. This never drains our own exclusive OP gate.
	scope.stop()
	return scope.finishTerminal(budget, current, callbackErr)
}

func (s *hostForgetMembership) enter() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active.Load() || s.terminal != nil {
		return nil, hostStateRemovalError("the verified membership scope ended or sealed its terminal inventory")
	}
	s.workers.Add(1)
	return s.workers.Done, nil
}
func (s *hostForgetMembership) stop() {
	s.mu.Lock()
	s.active.Store(false)
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.workers.Wait()
}
func stateScopeEnter(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s, _ := ctx.Value(hostForgetMembershipKey{}).(*hostForgetMembership); s != nil {
		return s.enter()
	}
	return func() {}, nil
}

func committedHostForgetOriginal(ctx context.Context, original Host, held *heldLock, generation string) (Host, error) {
	if intent, _ := ctx.Value(hostForgetIntentKey{}).(*hostForgetIntent); intent != nil {
		if SameEntry(intent.original, original) {
			return intent.original, nil
		}
		return Host{}, hostStateRemovalError("the committed cleanup changed its immutable original")
	}
	held.forgetMu.Lock()
	defer held.forgetMu.Unlock()
	if witness := held.forgotten; witness != nil && witness.generation == generation && SameEntry(witness.original, original) {
		return witness.original, nil
	}
	return Host{}, hostStateRemovalError("the committed cleanup has no immutable original witness")
}
