package host

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func restoreNoneForGateV1(ctx context.Context, gate *restoreGateV1, operation *SessionOperation, saved *Manifest, boot string, all bool) (result RestoreResult, present bool) {
	result = RestoreResult{ID: saved.ID, Name: saved.Name}
	switch {
	case saved.Ended():
		result.Action, result.Why = "skipped", "the agent had ended"
		return result, true
	case saved.Restart.Policy != "resume":
		result.Action, result.Why = "skipped", "restart policy is "+saved.Restart.Policy
		return result, true
	case !all && saved.Run.BootID == boot:
		result.Action, result.Why = "skipped", "no restart since it stopped (use --all)"
		return result, true
	case saved.Restart.MaxAttempts > 0 && saved.Run.RespawnCount >= saved.Restart.MaxAttempts:
		result.Action, result.Why = "skipped", fmt.Sprintf("already restored %d times", saved.Run.RespawnCount)
		return result, true
	}
	cold, err := captureNoneAutomaticContinuationV1(ctx, operation, gate)
	if cold != nil {
		// Adopt the SAME actual partial before inspecting its constructor
		// error. Native uncertainty keeps this original OP and restore gate.
		defer func() {
			reported := false
			for {
				unknown := cold.Close()
				if unknown == nil {
					break
				}
				if !reported {
					warnSession(operation.Primary(), "automatic None cleanup retains its original operation and resources", "err", unknown)
					reported = true
				}
				time.Sleep(250 * time.Millisecond)
			}
			if diagnostic := cold.CloseDiagnosticsV4(); diagnostic != nil {
				warnSession(operation.Primary(), "automatic None cleanup reported an original I/O outcome", "err", diagnostic)
			}
		}()
	}
	if err != nil {
		return noneRestoreFailureV1(result, err), true
	}
	if !all {
		working, err := sandboxWasWorkingV1(saved)
		if err != nil {
			return noneRestoreFailureV1(result, err), true
		}
		if !working {
			if err := stopLazyNoneForGateV1(ctx, operation, cold); err != nil {
				return noneRestoreFailureV1(result, err), true
			}
			result.Action, result.Why = "stopped", "the unwalled session was idle; attach to wake it"
			return result, true
		}
	}
	argv, why, err := cold.restoreFromGateV1(ctx, operation)
	result.Argv = argv
	if err != nil {
		return noneRestoreFailureV1(result, err), true
	}
	result.Action, result.Why = "restored", why
	return result, true
}

func noneRestoreFailureV1(result RestoreResult, err error) RestoreResult {
	result.Action, result.Why, result.Hint = "failed", err.Error(), "Keep the original restore operation and inspect the unwalled creator before retrying."
	var failure *proto.Error
	if errors.As(err, &failure) && failure.Hint != "" {
		result.Hint = failure.Hint
	}
	return result
}
