//go:build linux

package host

import (
	"context"
	"errors"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

func (child *forkChildV1) start(ctx context.Context) (_ *SessionInfo, failure error) {
	if child == nil || child.source == nil || child.source.closed || !child.source.copied || child.closed || child.started || child.reservation == nil || child.operation == nil || child.operation.native == nil || child.operation.native.borrowed || child.operation.Primary() != child.reservation.id || child.manifest.ID != child.reservation.id || !child.operation.Valid(child.reservation.id) {
		return nil, forkFailureV1("fork_destination", "the child has no original copied reservation")
	}
	source := child.source
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if err := child.operation.native.rootRef.CheckRoot(child.reservation.dir); err != nil {
		return nil, err
	}
	m := &child.manifest
	if err := ValidateManifestStart(m); err != nil {
		return nil, err
	}
	// A distinct private fork writer consumes actual source and destination
	// owners. A copied manifest never enters the ordinary New witness path.
	if err := withPlacement(ctx, false, func(control *placementControl) error {
		if err := checkUserDrainLockedV1(ctx, control); err != nil {
			return err
		}
		if err := applyPersistentLockdownLockedV1(ctx, control, m); err != nil {
			return err
		}
		owner := source.warm.owner
		if owner == nil || owner.operation != source.operation || owner.closed || owner.stopSent || owner.settled || owner.holder == nil || owner.invocation == nil {
			return forkFailureV1("fork_source", "the original native source ended before child admission")
		}
		if err := errors.Join(source.operation.native.check(ctx), owner.origin.checkIdentity(), owner.holder.check(ctx), owner.invocation.Check(ctx), source.operation.native.rootRef.CheckRoot(owner.holder.root), child.operation.native.check(ctx)); err != nil {
			return err
		}
		if err := admitNewMemoryLockedV1(ctx, child.operation, m); err != nil {
			return err
		}
		return child.operation.saveNewManifest(m)
	}, false); err != nil {
		return nil, err
	}
	// Native start retains the same new reservation/OP. The original child's
	// egress producer creates its own CA/stand-ins. Values and signer rights
	// are absent until an ordinary authenticated attach supplies them.
	started, launch, err := startOwnedSessionOperation(ctx, child.operation, nil, nil, true, initialDelivery(m))
	if launch != nil {
		child.reservation.launched = launch.started
		m.launchStarted = launch.started
		m.Run.Unit, m.Run.HolderPID = launch.manifest.Run.Unit, launch.manifest.Run.HolderPID
	}
	if err != nil {
		if launch != nil {
			cleanup := launch.Close()
			if launch.started && !(launch.cleanupConfirmed && launch.exited) {
				return nil, &keptError{err: UserError("start_failed", "fork child "+m.ID+" was retained because its original native cleanup is unresolved: "+errors.Join(err, cleanup).Error(), "Inspect that child's original runtime with the host doctor before resuming or removing it.")}
			}
			if launch.cleanupConfirmed && launch.exited {
				if checkErr := child.operation.native.check(context.Background()); checkErr == nil {
					_ = deleteLocked(m.ID, nil, &DeleteResult{})
				}
			}
			err = errors.Join(err, cleanup)
		}
		return nil, UserError("start_failed", "fork child "+m.ID+" could not start: "+err.Error(), "Read that child's supervisor log, then retry fork from the original source.")
	}
	if launch == nil || started == nil {
		return nil, forkFailureV1("start_failed", "the fork child has no actual native startup result")
	}
	defer func() {
		if cleanup := launch.Close(); cleanup != nil {
			failure = errors.Join(failure, UserError("fork_child_cleanup", "fork child "+m.ID+" started, but its original startup cleanup is unresolved: "+cleanup.Error(), "Keep that child's state and inspect its original runtime with the host doctor."))
		}
	}()
	child.started = true
	info := publicationInfoFrom(started, true)
	if err := checkSecretPublicationResponse(info); err != nil {
		return nil, err
	}
	return &info, nil
}

func forkContextV1(ctx context.Context, args api.ForkArgs) (result *api.ForkResult, returned error) {
	if ctx == nil {
		return nil, forkFailureV1("fork_source", "fork has no original request lifetime")
	}
	if err := errors.Join(CheckCaller(), ctx.Err(), args.Validate()); err != nil {
		return nil, err
	}
	source, err := captureForkSourceV1(ctx, args.ID)
	if err != nil {
		return nil, err
	}
	defer func() {
		returned = errors.Join(returned, source.Close())
		if returned == nil {
			return
		}
		var retained []string
		for _, child := range source.children {
			if child.started || child.reservation != nil && child.reservation.launched {
				retained = append(retained, child.manifest.ID)
			}
		}
		if len(retained) > 0 {
			returned = UserError("fork_incomplete", "fork did not finish; these children started or retain their original native runtime: "+strings.Join(retained, ", ")+". "+returned.Error(), "List the host's sessions and inspect these child IDs before retrying fork.")
		}
	}()
	if err := source.reserve(ctx, args.Count, args.Name); err != nil {
		return nil, err
	}
	if err := source.copy(ctx); err != nil {
		return nil, err
	}
	result = &api.ForkResult{Parent: args.ID, Children: []api.SessionInfo{}}
	for index, child := range source.children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := child.start(ctx)
		if err != nil {
			failure := forkResultErrorV1(err)
			result.Failures = append(result.Failures, api.ForkFailure{Index: index + 1, Error: failure})
			continue
		}
		result.Children = append(result.Children, *info)
	}
	if err := result.Validate(args); err != nil {
		return nil, err
	}
	return result, nil
}
