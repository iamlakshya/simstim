package client

import (
	"context"
	"errors"
)

// This purpose-only seam clears an owner-validated destroy-only journal after
// exact original forget. It cannot bootstrap creation, erase a purge receipt,
// remove sentinels/folders/OP, or turn name/Done/absence into owner authority.
// H15's one pure canonical owner validator decides eligible receipt semantics.
func removeHostRemovalRecordContext(ctx context.Context, original Host, validate func(stateFileView) error) (removed bool, failure error) {
	scope, _ := ctx.Value(hostForgetMembershipKey{}).(*hostForgetMembership)
	if scope == nil || !scope.active.Load() || scope.name != original.Name || !SameEntry(scope.original, original) || validate == nil {
		return false, hostStateRemovalError("removal-record clearing needs its exact active committed original")
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return false, err
	}
	failure = withStateTransaction(ctx, []string{"hosts", HostFileKey(original.Name)}, "removal.json", false, true, func(tx *stateFileTxn) error {
		if tx.source == nil {
			return nil
		}
		if err := tx.removalOwnership(); err != nil {
			return err
		}
		if err := validate(tx.view()); err != nil {
			return err
		}
		if err := tx.removalOwnership(); err != nil {
			return err
		}
		tx.revokeReader()
		err := tx.removeExact()
		removed = tx.removed
		return err
	})
	return removed, failure
}

// removeExact is the original shared FDv2 checked unlink primitive. Its
// nonterminal callers have only fixed removal.json purposes and actual scopes.
func (tx *stateFileTxn) removeExact() error {
	if tx.source == nil {
		return nil
	}
	beforeHostStateUnlink(tx.name)
	if err := tx.removalOwnership(); err != nil {
		return err
	}
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	if err := tx.root.Remove(tx.name); err != nil {
		return err
	}
	tx.removed = true
	// The view has ended. Close our deleted source before native namespace
	// rechecks so Windows fallback delete-pending is not prolonged by us.
	tx.revokeReader()
	source := tx.source
	tx.source = nil
	tx.before = nil
	closeErr := closeStateOwnedFile(tx.ctx, source)
	return errors.Join(closeErr, tx.finishCommittedMutation())
}
