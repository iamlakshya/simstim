//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// The actual closed child constructs this object BEFORE any OP or native
// subject capture. Its same objects, not journal rows, survive every Continue.
type purgeActiveTransactionV2 struct {
	mu            sync.Mutex
	lifetime      context.Context
	args          api.PurgeArgs
	owner         *purgeOwnerV1
	origin        *purgeRetainedOriginV2
	subjects      []*purgeOrdinarySubjectV2
	closed        bool
	aborted       bool
	captured      bool
	globalStarted bool
	pendingAbort  error
	finalGuard    *purgeFinalGuardV2
	closure       *purgeTransactionClosureV3
	abortClosure  *purgeTransactionClosureV3
}

type purgeOrdinarySubjectV2 struct {
	operation   *SessionOperation
	manifest    *Manifest
	preparation *OrdinaryPurgePreparationV2
	permit      *ordinaryActiveGitInspectionPermitV2
	initial     *joinedOrdinaryGitInspectionV2
	final       *joinedOrdinaryGitInspectionV2
	workLoss    *purgeOrdinaryWorkLossV2
	stopped     *StoppedOrdinarySessionV1
	terminal    *purgeOrdinaryTerminalV2
	worktree    *ordinaryWorktreeCleanupV1
	directory   purgeRef
}

func newOwnedPurgeTransactionV2(lifetime context.Context, args api.PurgeArgs) (*purgeActiveTransactionV2, error) {
	if lifetime == nil || !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) {
		return nil, placementError("the actual purge child has no original lifetime and authorised attempt")
	}
	if err := errors.Join(lifetime.Err(), CheckCaller()); err != nil {
		return nil, err
	}
	return &purgeActiveTransactionV2{lifetime: lifetime, args: args}, nil
}

func (transaction *purgeActiveTransactionV2) captureV2(ctx context.Context) error {
	if transaction.owner == nil {
		// A new child never adopts a partly destructive saved attempt. Only an
		// authenticated continuation of the actual existing object may do so.
		if err := withPlacementRoot(ctx, false, func(control *placementControl, _ *os.Root) error {
			if control.Destructive || len(control.Sessions) != 0 || control.Phase == "finalising" {
				return placementError("the saved removal has no new-capture route; retain its original actual attempt")
			}
			return nil
		}, false); err != nil {
			return err
		}
		owner, err := beginPurgeOwnerV1(ctx, transaction.args)
		transaction.owner = owner // Adopt the real owner BEFORE handling error.
		if owner != nil {
			owner.ctx = context.WithValue(transaction.lifetime, purgeActualTransactionKeyV2{}, transaction)
		}
		if err != nil {
			return err
		}
	}
	owner := transaction.owner
	if err := waitPlacementOperations(ctx, owner.placement); err != nil {
		return err
	}
	if owner.roots == nil {
		if err := owner.pinRoots(); err != nil {
			return err
		}
	}
	if transaction.origin == nil {
		origin, err := newPurgeRetainedOriginV2(ctx, owner)
		transaction.origin = origin
		if err != nil {
			return err
		}
	}
	if len(transaction.subjects) == 0 {
		manifests, err := trustedSessionManifests()
		if err != nil {
			return err
		}
		sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
		for _, manifest := range manifests {
			if manifest.SandboxOf != "" {
				continue
			}
			operation, err := BeginSessionOperation(ctx, manifest.ID, time.Second)
			if operation != nil {
				transaction.subjects = append(transaction.subjects, &purgeOrdinarySubjectV2{operation: operation})
			}
			if err != nil {
				return err
			}
		}
	}
	preparedContext, err := transaction.origin.contextV2(ctx)
	if err != nil {
		return err
	}
	// Acquire and retain ALL primary operations before ANY reversible pause.
	for _, subject := range transaction.subjects {
		if subject.operation == nil {
			return sessionLeaseError("the actual purge vector lost an original operation")
		}
		if subject.manifest == nil {
			manifest, err := subject.operation.LoadContext(ctx)
			if err != nil {
				return err
			}
			subject.manifest = manifest
			if !ordinaryManifestV1(manifest) || len(manifest.extra) != 0 {
				// This bounded ordinary implementation cannot manufacture a
				// walled controller. A genuine separate producer is required.
				return UserError("purge", "a selected session needs its original walled active preparation", "Keep the host saved and use the complete matching native purge controller.")
			}
		}
		if subject.preparation == nil {
			preparation, err := PrepareOrdinaryPurgeForOperationV2(preparedContext, subject.operation)
			subject.preparation = preparation
			if err != nil {
				return err
			}
			if preparation.owner.managed != nil {
				return UserError("purge", "a managed session needs its original whole-unit outside restoration participant", "Keep the host saved and finish the matching managed native preparation.")
			}
		}
		if subject.manifest.Git != nil && !transaction.args.Force && subject.permit == nil {
			permit, err := owner.beginOrdinaryActiveGitInspectionV2(ctx, subject.operation, subject.preparation)
			subject.permit = permit
			if err != nil {
				return err
			}
		}
		if subject.manifest.Git != nil && subject.worktree == nil {
			worktree, err := captureOriginalPurgeWorktreeV3(ctx, owner, subject.operation, subject.manifest)
			subject.worktree = worktree
			if err != nil {
				return err
			}
		}
	}
	transaction.captured = true
	return nil
}

// Continue performs one serial request against the same in-memory vector.
// Client cancellation before dispatch restores every original subject. After
// actual dispatch the owner lifetime, not the transport, retains cleanup.
func (transaction *purgeActiveTransactionV2) Continue(request context.Context) (api.PurgeResult, error) {
	if transaction == nil || request == nil {
		return api.PurgeResult{}, placementError("the actual purge continuation is absent")
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.closed || transaction.aborted {
		return transaction.reportV2(), placementError("this actual purge transaction is already closed or restored")
	}
	ctx := request
	if transaction.anyIrreversibleV2() {
		ctx = transaction.lifetime
	}
	if transaction.pendingAbort != nil {
		return transaction.reportV2(), errors.Join(transaction.pendingAbort, transaction.abortBeforeStopLockedV2(context.Background()))
	}
	if !transaction.captured {
		if err := transaction.captureV2(ctx); err != nil {
			return transaction.beforeStopFailureV2(err)
		}
	}
	if !transaction.anyStopV2() {
		if err := transaction.inspectAllV2(ctx, false); err != nil {
			return transaction.beforeStopFailureV2(err)
		}
		for _, subject := range transaction.subjects {
			if err := subject.preparation.Quiesce(ctx, subject.operation); err != nil {
				return transaction.beforeStopFailureV2(err)
			}
		}
		if err := transaction.inspectAllV2(ctx, true); err != nil {
			return transaction.beforeStopFailureV2(err)
		}
		// Every final source is checked before the FIRST irreversible stop.
		for _, subject := range transaction.subjects {
			if err := subject.preparation.CheckQuiesced(ctx, subject.operation); err != nil {
				return transaction.beforeStopFailureV2(err)
			}
			if subject.worktree != nil {
				if err := subject.worktree.CheckBeforeStop(ctx); err != nil {
					return transaction.beforeStopFailureV2(err)
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return transaction.beforeStopFailureV2(err)
		}
		if !transaction.owner.phaseDone("precheck") {
			if err := transaction.owner.completePhase("precheck"); err != nil {
				return transaction.beforeStopFailureV2(err)
			}
		}
	}
	for _, subject := range transaction.subjects {
		if subject.stopped == nil {
			commitContext := request
			if transaction.anyStopV2() {
				commitContext = transaction.lifetime
			}
			stopped, err := subject.preparation.CommitStop(commitContext, subject.operation)
			subject.stopped = stopped // Same real source retained even with lost ACK.
			if err != nil {
				if !transaction.anyStopV2() {
					return transaction.beforeStopFailureV2(err)
				}
				return transaction.reportV2(), err
			}
		}
		if err := transaction.cleanupOrdinaryV2(transaction.lifetime, subject); err != nil {
			return transaction.reportV2(), err
		}
	}
	if !transaction.owner.phaseDone("sessions") {
		if err := transaction.owner.completePhase("sessions"); err != nil {
			return transaction.reportV2(), err
		}
	}
	// This actual invocation retains the native root-cleanup owner even in
	// the zero-session case. It grants no stop or deletion authority.
	transaction.globalStarted = true
	for _, finish := range []func() error{transaction.owner.undoFootprints, transaction.owner.removeOwnedRoots, transaction.owner.removeOwnedHelpers, transaction.owner.prepareFinalGuard} {
		if err := finish(); err != nil {
			return transaction.reportV2(), err
		}
	}
	return transaction.reportV2(), nil
}

func (transaction *purgeActiveTransactionV2) inspectAllV2(ctx context.Context, final bool) error {
	var unsaved []api.PurgeSession
	for _, subject := range transaction.subjects {
		current, err := subject.operation.LoadContext(ctx)
		if err != nil {
			return err
		}
		if err := subject.preparation.Check(ctx, subject.operation); err != nil {
			return err
		}
		if transaction.args.Force {
			continue
		}
		if current.CopiesPending || current.Status.State == StateWorking {
			unsaved = append(unsaved, api.PurgeSession{ID: current.ID, Name: current.Name, Reason: "the original session reports work which has not settled or been pulled"})
		}
		if current.Git == nil {
			continue
		}
		inspection, err := inspectOrdinaryActiveGitV2(ctx, subject.operation, subject.preparation, subject.permit)
		if final {
			subject.final = inspection
		} else {
			subject.initial = inspection
		}
		if err != nil {
			return err
		}
		counts, err := inspection.Counts(ctx, subject.operation, subject.preparation)
		if err != nil {
			return err
		}
		if counts.Dirty != 0 || counts.Unpushed != 0 {
			unsaved = append(unsaved, api.PurgeSession{ID: current.ID, Name: current.Name, Reason: "the original worktree has changed files or unpushed commits"})
		}
		if final && counts.Dirty == 0 && counts.Unpushed == 0 {
			proof, err := sealOrdinaryWorkLossV2(ctx, transaction.owner, subject.operation, subject.preparation, subject.permit, inspection)
			subject.workLoss = proof
			if err != nil {
				return err
			}
		}
	}
	if len(unsaved) == 0 {
		return nil
	}
	transaction.owner.report.Unsaved = unsaved
	err := UserError("unsaved", "host removal found work which has not been saved or pulled", "Save the listed work, or use --force after reviewing what will be lost.")
	err.Data, _ = json.Marshal(unsaved)
	return err
}

func (transaction *purgeActiveTransactionV2) anyStopV2() bool {
	for _, subject := range transaction.subjects {
		// These are the SAME actual producers recording physical send, never
		// a phase or a copied row used to reconstruct physical ownership.
		if subject.stopped != nil || subject.preparation != nil && subject.preparation.owner != nil && subject.preparation.owner.stopSent {
			return true
		}
	}
	return false
}

func (transaction *purgeActiveTransactionV2) reportV2() api.PurgeResult {
	if transaction.owner == nil {
		return api.PurgeResult{HostID: transaction.args.HostID, Nonce: transaction.args.Nonce}
	}
	return transaction.owner.report
}
