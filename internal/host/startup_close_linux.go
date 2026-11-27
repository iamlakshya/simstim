//go:build linux

package host

import "errors"

func (launch *startupLaunchV1) Close() error {
	if launch == nil {
		return nil
	}
	if launch.buildingDone != nil {
		<-launch.buildingDone
	}
	launch.mu.Lock()
	defer launch.mu.Unlock()
	if launch.closed {
		return launch.closeErr
	}
	if !launch.ready && !launch.nativeJoined && !launch.exited {
		if launch.line != nil && !launch.cancelAttempted {
			launch.cancelAttempted = true // No second cancellation can renew it.
			launch.closeErr = errors.Join(launch.closeErr, launch.cancelPending())
		}
		// This original scoped owner retains OP while joining the real native
		// creation. Neither failed configuration cleanup nor caller cancellation
		// can detach it into a managerless custodian.
		err := launch.waitNativeExit()
		if err != nil && !launch.exited {
			return errors.Join(launch.closeErr, err)
		}
		launch.closeErr = errors.Join(launch.closeErr, launch.sandboxPrepareWaitFailureV1(err))
		launch.nativeJoined = true
	}
	if launch.line != nil {
		launch.closeErr = errors.Join(launch.closeErr, launch.line.Close())
		launch.line = nil
	}
	if launch.unit != nil {
		if err := launch.unit.Close(); err != nil {
			return errors.Join(launch.closeErr, err)
		}
		launch.unit = nil
	}
	if launch.child != nil {
		launch.closeErr = errors.Join(launch.closeErr, launch.child.Close())
		launch.child = nil
	}
	if launch.manager != nil {
		if err := closeNativeManagerResourceV4(launch.manager, launch.operation.Primary(), &launch.managerDiagnostic); err != nil {
			return errors.Join(launch.closeErr, err)
		}
		launch.manager = nil
	}
	if launch.command != nil && launch.ready && !launch.exited {
		launch.closeErr = errors.Join(launch.closeErr, launch.command.Process.Release())
		launch.command = nil
	}
	if launch.placement != nil {
		if err := launch.placement.close(); err != nil {
			return errors.Join(launch.closeErr, err)
		}
		launch.placement = nil
	}
	launch.closed = true
	return launch.closeErr
}
