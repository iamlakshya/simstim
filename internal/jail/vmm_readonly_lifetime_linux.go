//go:build linux

package jail

import (
	"context"
	"errors"
	"syscall"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func (c *Cmd) killReadonlyInitV1(signal syscall.Signal) error {
	if c == nil || signal != unix.SIGKILL {
		return nil
	}
	c.mu.Lock()
	owner := c.modeExecV1
	c.mu.Unlock()
	if owner == nil {
		return nil
	}
	return owner.killReadonlyActorV1(signal)
}

func (owner *vmmExecOwnerV1) killReadonlyActorV1(signal syscall.Signal) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.readonly || owner.readonlyPhysicalClosed || owner.init == nil {
		return nil
	}
	raw, err := owner.init.process.SyscallConn()
	if err != nil {
		return err
	}
	var native error
	err = raw.Control(func(fd uintptr) { native = ownedPidfdSignal(int(fd), signal, nil, 0) })
	if errors.Is(native, unix.ESRCH) {
		native = nil
	}
	return errors.Join(err, native)
}

func (c *Cmd) ReadonlyInitEndedV1(source *supply.ExecModeLeaseV1) (bool, error) {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return false, err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.readonly {
		return false, vmmModeFailureV1("the runtime has no original read-only init")
	}
	if owner.readonlyPhysicalClosed {
		return true, nil
	}
	if owner.init == nil {
		return false, vmmModeFailureV1("the original read-only namespace-init lifetime is unknown")
	}
	raw, err := owner.init.process.SyscallConn()
	if err != nil {
		return false, err
	}
	var ready bool
	var native error
	err = raw.Control(func(fd uintptr) { ready, native = ownedPIDFDReady(int(fd), 0) })
	return ready, errors.Join(err, native)
}

// The disk/runtime owner uses this separately from the successful artifact
// final receipt. Only actual original actor/cgroup closure can release it.
func (c *Cmd) CheckVMMReadonlyPhysicalV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	if !owner.readonly {
		return vmmModeFailureV1("the physical read-only scope belongs to another original source")
	}
	return owner.checkPhysicalClosedV1()
}

// Exit status is read only after this SAME Cmd's positive physical wait. It
// is never a readiness, process adoption or no-future-exec permission.
func (c *Cmd) ReadonlyExitCodeV1(source *supply.ExecModeLeaseV1) (int, string, error) {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return -1, "", err
	}
	owner.mu.Lock()
	readonly := owner.readonly && owner.readonlyPhysicalClosed
	owner.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !readonly || !c.physical || c.exit == nil {
		return -1, "", vmmModeFailureV1("the original read-only runtime has no physical exit result")
	}
	signal := ""
	if c.exit.Signal != 0 {
		signal = c.exit.Signal.String()
	}
	return c.exit.Code, signal, nil
}

func (owner *vmmExecOwnerV1) closeReadonlyLifetimeV1() error {
	if !owner.readonly {
		return nil
	}
	if err := owner.closeHostV1(); err != nil {
		return err
	}
	if owner.nativeStarted {
		<-owner.nativeDone
	}
	if owner.readonlyGroup != nil {
		if err := owner.readonlyGroup.Close(); err != nil {
			return err
		}
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.code == nil {
		return nil
	}
	if !owner.readonlyPhysicalClosed && !owner.noCreation {
		return vmmModeFailureV1("the original helper code still belongs to a live native read-only chain")
	}
	if !owner.readonlyCodeCloseAttempted {
		owner.readonlyCodeCloseAttempted = true
		owner.readonlyCodeClose = supply.CloseFileOnceV1(owner.code)
		if err := errors.Join(owner.readonlyCodeClose.Diagnostic, owner.readonlyCodeClose.Unknown); err != nil {
			return err
		}
	}
	if owner.readonlyCodeClose.Unknown != nil {
		return owner.readonlyCodeClose.Unknown
	}
	if !owner.readonlyCodeClose.Retired {
		return vmmModeFailureV1("the original helper code retirement remains unknown")
	}
	owner.code = nil
	return nil
}

// DisableUnstartedVMMV1 is destructive cleanup only. This actual Cmd can never
// create a child afterwards. It accepts no success/PID/source-role metadata.
func (c *Cmd) DisableUnstartedVMMV1() error {
	if c == nil {
		return vmmModeFailureV1("the original unstarted command is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase == phaseFailed && c.physical && !c.owned {
		return nil
	}
	if c.phase != phaseNew || c.owned || c.cmd == nil || c.cmd.Process != nil || c.modeExecV1 != nil {
		return vmmModeFailureV1("the actual VMM creation is not an unattached unstarted command")
	}
	c.phase, c.physical = phaseFailed, true
	return nil
}

// VMMCloseDiagnosticsV4 borrows only actual cached close diagnostics. It grants
// no original source, role, descriptor retirement or physical cleanup proof.
func (c *Cmd) VMMCloseDiagnosticsV4(source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return nil
	} // No actual diagnostic is available from this owner.
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.readonlyCodeClose.Diagnostic
}
