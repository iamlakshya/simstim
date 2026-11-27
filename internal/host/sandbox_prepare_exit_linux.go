//go:build linux

package host

import (
	"errors"
	"os/exec"
)

// Only the exact exit-status object from this SAME already-reaped command is
// diagnostic after native cleanup. Poll/wait/descriptor/manager failures stay
// errors. This is never applied to ordinary startup or an unconfirmed wait.
func (launch *startupLaunchV1) sandboxPrepareWaitFailureV1(failure error) error {
	if failure == nil || launch.operation == nil || !launch.operation.IsSandboxPrepareV1() ||
		launch.command == nil || launch.command.ProcessState == nil || !launch.exited {
		return failure
	}
	original := failure
	var keep func(error) error
	keep = func(err error) error {
		if err == nil {
			return nil
		}
		if exit, ok := err.(*exec.ExitError); ok && exit.ProcessState == launch.command.ProcessState {
			return nil
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			var remaining []error
			for _, child := range joined.Unwrap() {
				if kept := keep(child); kept != nil {
					remaining = append(remaining, kept)
				}
			}
			return errors.Join(remaining...)
		}
		return err
	}
	failure = keep(failure)
	if launch.operation.sandboxPrepare.bridge.log != nil {
		launch.operation.sandboxPrepare.bridge.log.Warn("fixed helper retained original exit diagnostics", "err", original)
	}
	return failure
}
