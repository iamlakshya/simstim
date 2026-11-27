//go:build linux

package host

import (
	"context"
	"errors"
)

// This bounded set describes only requests dispatched by the SAME original
// fixed preparation issuer. It grants no execution or cleanup permission.
func (launch *startupLaunchV1) queueSandboxPrepareReplyV1(id uint64) error {
	if launch.operation == nil || !launch.operation.IsSandboxPrepareV1() {
		return nil
	}
	if id != 1 && id != 2 && id != 3 && id != 5 || launch.prepareFailed || launch.preparePending != 0 {
		return sessionLeaseError("preparation tried to replace an original pending native response")
	}
	launch.preparePending = uint8(1 << id)
	return nil
}

func (launch *startupLaunchV1) finishSandboxPrepareReplyV1(id uint64, consumed bool, failure error) {
	if launch.operation == nil || !launch.operation.IsSandboxPrepareV1() {
		return
	}
	if consumed {
		if id != 4 {
			launch.preparePending &^= uint8(1 << id)
		}
		return
	}
	if failure == nil || launch.line == nil || launch.line.preparePoison ||
		!sandboxPrepareReadDeadlineV1(failure) && !errors.Is(failure, context.Canceled) && !errors.Is(failure, context.DeadlineExceeded) {
		launch.prepareFailed = true
	}
}

func (launch *startupLaunchV1) drainSandboxPrepareRepliesV1(ctx context.Context) error {
	if launch.prepareFailed || launch.line == nil || launch.line.preparePoison {
		return sessionLeaseError("the original preparation response reader has an irreversible protocol failure")
	}
	for _, id := range []uint64{1, 2, 3, 5} {
		if launch.preparePending&uint8(1<<id) == 0 {
			continue
		}
		_, err := launch.response(ctx, id, false)
		if launch.prepareFailed || launch.preparePending&uint8(1<<id) != 0 {
			return errors.Join(sessionLeaseError("the original pending preparation response did not complete"), err)
		}
		// A complete native refusal is an exit diagnostic. It supplies no
		// cleanup proof; only the separate original cancel ACK can do that.
		if err != nil && launch.operation.sandboxPrepare.bridge.log != nil {
			launch.operation.sandboxPrepare.bridge.log.Warn("original fixed test response failed", "request", id, "err", err)
		}
	}
	return nil
}
