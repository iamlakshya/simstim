package host

import (
	"context"
	"errors"
	"time"
)

// StopContext retains the original canonical operation until the existing
// native stop owner proves the supervisor and its whole unit physically gone.
// The returned row reports the selected member; it grants no stopped custody.
func StopContext(ctx context.Context, id string) (_ *SessionInfo, returned error) {
	if ctx == nil {
		return nil, UserError("bad_request", "stop has no request lifetime", "Retry stop with the current client.")
	}
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return nil, err
	}
	operation, err := BeginSessionOperation(ctx, id, renameWait)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, operation.CloseWithError()) }()
	primary, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if IsNoneSupervisor(primary) {
		return stopNoneContextForOperationV1(ctx, id, operation)
	}
	if primary.Wall == nil || primary.Wall.Driver == "plain" {
		return nil, UserError("wall_unavailable", "this session has no supported sandbox to stop", "Quit its agent, or select a walled session.")
	}
	stopped, stopErr := StopSessionForOperationV1(ctx, operation)
	if stopped != nil {
		defer func() { returned = errors.Join(returned, stopped.Close()) }()
	}
	if stopped == nil {
		if stopErr == nil {
			stopErr = UserError("stop_failed", "the native stop returned no original physical owner", "Keep the session and inspect its native lifetime with host doctor before retrying.")
		}
		return nil, stopErr
	}
	// Once the original native owner has physically settled an accepted stop,
	// cancellation cannot revoke that fact. Cleanup still holds the SAME OP.
	publication, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	primary, err = publishRequestedStopV1(publication, operation, stopped)
	if err != nil {
		return nil, errors.Join(stopErr, err)
	}
	if stopErr != nil || ctx.Err() != nil {
		return nil, errors.Join(stopErr, ctx.Err())
	}
	selected := primary
	if id != primary.ID {
		selected, err = LoadManifest(id)
		if err != nil {
			return nil, err
		}
		if selected.ID != id || selected.SandboxOf != primary.ID {
			return nil, snapshotChanged()
		}
	}
	if selected.Life == nil || selected.Life.Phase != LifeStopped || selected.Life.End == nil || selected.Life.End.Kind != EndStop || selected.Status.State != StateStopped {
		return nil, UserError("stop_failed", "the sandbox exited without a completed stop record", "Read its supervisor.log with host doctor before resuming it.")
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return nil, err
	}
	row := publicationInfoFrom(selected, false, primary)
	return &row, nil
}
