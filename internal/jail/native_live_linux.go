package jail

import (
	"context"

	"golang.org/x/sys/unix"
)

// CheckNativeLiveV1 checks THIS actual started command's retained creation
// lifetime. It never looks up a saved PID, adopts another process, revives a
// failed/uncertain owner or grants boot/stop/capacity permission. A caller
// using it for warm operations must separately retain the SAME actual Start
// connection, guest/runtime identity and original operation/native source.
func (c *Cmd) CheckNativeLiveV1(ctx context.Context) error {
	if c == nil || ctx == nil {
		return nativeLiveFailureV1("the original command or request lifetime is absent", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.owned || c.pidfd < 0 || c.pid <= 0 || c.reaped || c.physical || c.groupUnsafe || c.phase != phaseRunning && c.phase != phaseWaiting {
		return nativeLiveFailureV1("the exact native command owner is unstarted, ended or uncertain", nil)
	}
	if err := checkNativeLivePIDFDV1(c.pidfd); err != nil {
		return err
	}
	if c.gvisorOuter != nil {
		owner := c.gvisorOuter
		// The creator is not A. Both original creation handles and the verified
		// mapped-code bootstrap remain required; a current response PID is no proof.
		if owner.noChild || owner.child == nil || owner.childFD < 0 || int(owner.child.Fd()) != owner.childFD || owner.bootstrapIdentity == "" || owner.launcher == nil || owner.helper == nil {
			return nativeLiveFailureV1("the original gvisor creator/A/code chain is incomplete", nil)
		}
		if _, err := unix.FcntlInt(owner.launcher.Fd(), unix.F_GETFD, 0); err != nil {
			return nativeLiveFailureV1("the original native launcher handle closed", err)
		}
		if _, err := unix.FcntlInt(owner.helper.Fd(), unix.F_GETFD, 0); err != nil {
			return nativeLiveFailureV1("the original mapped helper handle closed", err)
		}
		if err := checkNativeLivePIDFDV1(owner.childFD); err != nil {
			return err
		}
		if owner.nested == nil {
			return nativeLiveFailureV1("the original nested C creation source is absent", nil)
		}
		if err := owner.nested.liveV1(); err != nil {
			return err
		}
		if err := owner.fork.checkReadyV2(); err != nil {
			return err
		}
	}
	return ctx.Err()
}
func checkNativeLivePIDFDV1(fd int) error {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if _, err := unix.Poll(poll, 0); err != nil {
		return nativeLiveFailureV1("the original creation descriptor could not be observed", err)
	}
	if poll[0].Revents != 0 {
		return nativeLiveFailureV1("the original creation descriptor reports exit or uncertainty", nil)
	}
	return nil
}
func nativeLiveFailureV1(message string, cause error) error {
	hint := "Keep the original command and native connection; use a supported stopped path only after its actual physical cleanup."
	if cause == nil {
		return failure("cannot retain a live native jail: "+message, hint)
	}
	return stepFailed("cannot retain a live native jail: "+message, hint, cause)
}
