//go:build linux

package host

import (
	"context"
	"errors"
)

// consumeStoppedColdHoldersV1 runs only in the existing native dispatch gate
// with Cold.mu held. The two leases below are the exact original Stop owner
// and its F_DUPFD-created vacancy copy. No flag, ACK, journal or named lock
// can create or replace either one.
func consumeStoppedColdHoldersV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1) (bool, error) {
	if cold == nil || cold.vacancy == nil {
		return false, auxiliaryUnavailableV1("cold dispatch has no original native vacancy")
	}
	vacancy := cold.vacancy
	owner := vacancy.stopped
	if owner == nil {
		return false, nil
	}
	if ctx == nil || operation == nil || operation.nativeColdResume != cold || cold.operation != operation || vacancy.operation != operation || !cold.bound || cold.handed || cold.closed || !operation.Valid(operation.Primary()) {
		return true, auxiliaryUnavailableV1("the stopped holder handoff lost its same bound Cold operation")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.operation != operation || !owner.stopSent || !owner.settled || owner.closed || owner.holderHanded || owner.holder == nil || owner.holder != vacancy.stoppedOriginalHolder || vacancy.holder == nil || vacancy.holder != vacancy.stoppedVacancyHolder || owner.invocation == nil || owner.manager == nil || owner.origin == nil || owner.boot != BootID() {
		return true, auxiliaryUnavailableV1("the actual stopped holder copies changed or were already consumed")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.origin.checkIdentity(), owner.holder.check(ctx), vacancy.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.holder.root), operation.native.rootRef.CheckRoot(vacancy.holder.root), owner.checkStoppedNativeFactsV1(ctx)); err != nil {
		return true, err
	}
	// Close never LOCK_UNs the shared description. On either error both
	// original owner objects remain reachable and no handoff is recorded.
	if err := owner.holder.Close(); err != nil {
		return true, err
	}
	if err := vacancy.holder.Close(); err != nil {
		return true, err
	}
	owner.holder = nil
	owner.holderHanded = true
	vacancy.holder = nil
	vacancy.transferred = true
	return true, nil
}
