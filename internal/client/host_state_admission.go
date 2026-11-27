package client

import (
	"context"
	"sync"
)

// acquireHostStateDomain runs before the canonical per-file process mutex.
// Global state retains its old acquisition. Every host-domain acquisition,
// including a read which might create a sentinel, needs the actual owner.
func acquireHostStateDomain(ctx context.Context, subdirs []string, name string, write, existingLock bool) (context.Context, func(), error) {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	hostDomain := len(subdirs) != 0 && subdirs[0] == "hosts"
	observedLedger := len(subdirs) == 0 && name == grantsFileName && (write || o != nil)
	if !hostDomain && !observedLedger {
		return ctx, func() {}, nil
	}
	if hostDomain && len(subdirs) != 2 {
		return ctx, nil, hostStateRemovalError("the host state domain is not canonical")
	}
	if inspection, _ := ctx.Value(hostTerminalInspectionKey{}).(*hostTerminalInspection); inspection != nil {
		if err := inspection.permits(ctx, subdirs, name, write, existingLock); err != nil {
			return ctx, nil, err
		}
		return ctx, func() {}, nil
	}
	if inspection, _ := ctx.Value(hostStateInspectionKey{}).(*hostStateInspection); inspection != nil {
		if err := inspection.check(ctx, subdirs, name, write, existingLock); err != nil {
			return ctx, nil, err
		}
		return ctx, func() {}, nil
	}
	if scope, _ := ctx.Value(hostForgetMembershipKey{}).(*hostForgetMembership); scope != nil {
		if !scope.active.Load() || hostDomain && subdirs[1] != HostFileKey(scope.name) {
			return ctx, nil, hostStateRemovalError("the verified cleanup belongs to another host domain")
		}
		if err := requireHostMembershipContext(ctx); err != nil {
			return ctx, nil, err
		}
		if err := stateContextAuthority(ctx); err != nil {
			return ctx, nil, err
		}
		return ctx, func() {}, nil
	}
	allowed := o != nil && o.permits(subdirs, name, write)
	if observedLedger {
		allowed = o != nil && (o.role == hostStateSaved || o.role == hostStateRead && !write)
	}
	if !allowed {
		return ctx, nil, hostStateRemovalError("the host state operation has no matching observation")
	}
	if write && o.role == hostStateSaved && (o.hasLegacyBinding() || name == "sandboxes.json" || name == "root-steps.json" || observedLedger) && !o.hasCheckedIdentity() {
		permit, _ := ctx.Value(legacyStateWriteKey{}).(*legacyStateWritePermit)
		if err := permit.consume(ctx, o, subdirs, name); err != nil {
			return ctx, nil, err
		}
	}
	if err := o.checkOwner(ctx); err != nil {
		return ctx, nil, err
	}
	owned := WithHostStateObservation(ctx, o)
	endOP := func() {}
	if o.role == hostStateRegistration {
		// LockHost must validate this existing registration source BEFORE
		// creating its OP inode. It must not mint a replacement observation.
		var err error
		owned, endOP, err = LockHost(owned, o.name, "host registration state")
		if err != nil {
			return ctx, nil, err
		}
	}
	held, _ := owned.Value(heldKey{o.name}).(*heldLock)
	if held == nil || held.gone.Load() || (o.role == hostStateSaved || o.role == hostStateRead) && held != o.op {
		endOP()
		return ctx, nil, hostStateRemovalError("the original observed operation is not held")
	}
	lifetime := held.stateLifetime()
	var leave func()
	var err error
	txctx := owned
	if mutation, _ := owned.Value(hostMutationContextKey{}).(*hostMutationScope); mutation != nil {
		txctx, leave, err = mutation.enter(owned, o.name, held)
	} else {
		txctx, leave, err = lifetime.enterTransaction(owned)
	}
	if err != nil {
		endOP()
		return ctx, nil, err
	}
	locked, endMembership, err := acquireHostMembershipContext(txctx)
	if err != nil {
		leave()
		endOP()
		return ctx, nil, err
	}
	cleanup := sync.OnceFunc(func() { endMembership(); leave(); endOP() })
	if err := o.checkMembership(locked); err != nil {
		cleanup()
		return ctx, nil, err
	}
	var current *hostMembershipSnapshot
	if o.source.exists {
		current, err = openHostMembershipSnapshot(locked)
		if err != nil {
			cleanup()
			return ctx, nil, err
		}
		if current.generation != o.source.generation || o.source.root.CheckRoot(current.root) != nil {
			current.close()
			cleanup()
			return ctx, nil, hostStateRemovalError("saved hosts changed before the state transaction")
		}
	}
	prior, _ := locked.Value(stateAuthorityKey{}).(func() error)
	bound := context.WithValue(locked, stateAuthorityKey{}, func() error {
		if err := requireHostMembershipContext(locked); err != nil {
			return err
		}
		if err := o.checkOwner(locked); err != nil {
			return err
		}
		if held.gone.Load() {
			return hostStateRemovalError("the state transaction's physical operation is ending")
		}
		if prior != nil {
			if err := prior(); err != nil {
				return err
			}
		}
		if current != nil {
			return current.check(locked)
		}
		return o.source.check(locked)
	})
	return bound, sync.OnceFunc(func() {
		if current != nil {
			current.close()
		}
		cleanup()
	}), nil
}
