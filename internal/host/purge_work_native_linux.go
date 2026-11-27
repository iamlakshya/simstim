//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/iamlakshya/simstim/internal/proto"
)

// CheckPurgeUnsavedForOperationV1 keeps the SAME original stopped operation
// through a newly accounted actual auxiliary reader and its physical join.
// The caller owns force-work-loss authorisation; force never enters this body.
func CheckPurgeUnsavedForOperationV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1) (returned error) {
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	if err := checkPurgeAuxiliaryAdmissionV1(ctx); err != nil {
		return err
	}
	snapshot, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	plan, err := protectedPurgeWorkPlanV1(snapshot)
	if err != nil {
		return err
	}
	vacancy, err := captureAuxiliaryVacancyFromStoppedV1(ctx, operation, stopped)
	if err != nil {
		return err
	}
	owner, err := prepareAuxiliaryOperationFromVacancyV1(ctx, operation, vacancy)
	if err != nil {
		if owner != nil {
			err = errors.Join(err, owner.Close())
		}
		return err
	}
	owner, err = openPreparedAuxiliaryGuestV1(ctx, owner)
	if err != nil {
		return err
	}
	// The existing Close owns the actual helper creation, full native unit,
	// reservation, artifact descriptors and original OP/holder loans. Unknown
	// physical settlement retains that real source stack instead of returning.
	closed := false
	defer func() {
		if !closed {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	var raw json.RawMessage
	if err := owner.Call(ctx, proto.CtlPurgeWorkInspectV1, plan, &raw); err != nil {
		return err
	}
	unsaved, err := decodePurgeWorkResultV1(raw, plan)
	if err != nil {
		return err
	}
	if err := owner.Close(); err != nil {
		return err
	}
	closed = true
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	fresh, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	freshPlan, err := protectedPurgeWorkPlanV1(fresh)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(freshPlan, plan) {
		return purgeWorkUnknownV1("the protected seed or pull receipts changed during the original inspection")
	}
	if unsaved {
		return UserError("unsaved", "the sandbox has unpulled commits, changed files or files outside its protected repositories", "Pull its committed work and copy its remaining files, then retry purge; use --force only to discard them.")
	}
	return ctx.Err()
}
