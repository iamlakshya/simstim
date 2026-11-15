//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/version"
)

// startupLaunchV1 is created only by the real outside operation launcher.
// Its original unreaped child or first managed activation stays owned through
// handoff, start delivery, readiness and synchronous failure cleanup.
type startupLaunchV1 struct {
	buildingDone     chan struct{}
	operation        *SessionOperation
	placement        *runtimePublication
	manifest         *Manifest
	executable       os.FileInfo
	argv             []string
	manager          *nativeUserManagerV1
	unit             *nativeUnitActivationV1
	command          *exec.Cmd
	child            *nativeStartupPeerV1
	line             *StartupLineV1
	hello            proto.Hello
	started          bool
	loanSent         bool
	ready            bool
	exited           bool
	readFailed       bool
	cleanupConfirmed bool
	mu               sync.Mutex
	once             sync.Once
	closeErr         error
}

func launchSupervisorOperation(ctx context.Context, operation *SessionOperation) (_ *startupLaunchV1, failure error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, sessionLeaseError("the outside startup has no original owning operation")
	}
	if err := errors.Join(CheckCaller(), CheckStartContext(ctx), ctx.Err()); err != nil {
		return nil, err
	}
	m, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if m.Wall == nil || m.SandboxOf != "" || m.Status.State != StateStarting {
		return nil, sessionLeaseError("the outside startup does not own a pending protected primary")
	}
	if err := ValidateManifestStart(m); err != nil {
		return nil, err
	}
	if len(m.AutomaticRestore) != 0 && operation.AutomaticBoot() == nil {
		return nil, automaticRestoreError()
	}
	origin, err := placementForCall()
	if err != nil {
		return nil, err
	}
	if err := origin.checkIdentity(); err != nil {
		return nil, err
	}
	sub, _, err := sessionCommand(m)
	if err != nil {
		return nil, err
	}
	argv := []string{origin.executable, sub, m.ID}
	launch := &startupLaunchV1{buildingDone: make(chan struct{}), operation: operation, manifest: m, executable: origin.executableRef, argv: append([]string(nil), argv...)}
	defer close(launch.buildingDone)
	operation.gate.Lock()
	if !operation.active.Load() || operation.startup != nil {
		operation.gate.Unlock()
		return nil, sessionLeaseError("the original operation already owns a native startup or ended")
	}
	operation.startup = launch
	operation.gate.Unlock()
	placement, err := beginStartupPublication(operation.ownerContext, operation)
	if err != nil {
		return launch, err
	}
	launch.placement = placement
	// The manager's actual lifetime is this scoped launch, not the shorter
	// caller deadline. Cancellation cannot destroy an unresolved native owner.
	launch.manager, err = openNativeUserManagerV1(ctx)
	if errors.Is(err, os.ErrNotExist) {
		launch.manager = nil
		if err := launch.startChild(ctx, m, origin.executable, origin.executableRef, argv); err != nil {
			return launch, err
		}
	} else if err != nil {
		return launch, err
	} else {
		if err := ctx.Err(); err != nil {
			return launch, err
		}
		launch.unit, err = launch.manager.startSupervisor(launch.placement.ctx, m, origin.executable, argv, launch.placement)
		launch.started = launch.manager.events.wasDispatched()
		if err != nil {
			return launch, err
		}
		m.Run.Unit = launch.unit.unit
	}
	if err := launch.connect(ctx); err != nil {
		return launch, err
	}
	if err := launch.sendOperationLoan(ctx); err != nil {
		return launch, err
	}
	return launch, nil
}

func (launch *startupLaunchV1) startChild(ctx context.Context, m *Manifest, executable string, expected os.FileInfo, argv []string) error {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	lock, err := openHolderLockForLaunch(m.ID)
	if err != nil {
		return err
	}
	defer lock.Close()
	pidfd := -1
	command := exec.Command(executable, argv[1:]...)
	command.Stdin, command.Stdout, command.Stderr = devnull, devnull, devnull
	command.Env = append(supervisorLaunchEnv(""), holderLockFDEnv+"=3")
	command.ExtraFiles = []*os.File{lock}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, PidFD: &pidfd}
	if err := launch.placement.gate(ctx, func() error { return startSessionCommand(m, command) }); err != nil {
		return err
	}
	launch.command, launch.started = command, true
	if pidfd < 0 {
		return sessionLeaseError("the original helper child has no creation lifetime handle")
	}
	launch.child = &nativeStartupPeerV1{pid: command.Process.Pid, uid: uint32(os.Getuid()), pidfd: pidfd}
	proc, err := os.OpenRoot(fmt.Sprintf("/proc/%d", command.Process.Pid))
	if err != nil {
		return err
	}
	launch.child.proc = proc
	if err := checkStartupExecutable(proc, expected, argv); err != nil {
		return err
	}
	m.Run.HolderPID = command.Process.Pid
	return launch.child.checkLive()
}

func (launch *startupLaunchV1) check(ctx context.Context) error {
	if launch == nil || launch.operation == nil || !launch.operation.Valid(launch.operation.Primary()) {
		return sessionLeaseError("startup lost its original operation owner")
	}
	if launch.placement == nil || launch.placement.ctx.Err() != nil {
		return sessionLeaseError("the original startup placement operation ended")
	}
	if err := errors.Join(CheckCaller(), launch.operation.native.check(ctx), ctx.Err()); err != nil {
		return err
	}
	if launch.unit != nil {
		return launch.unit.Check(ctx)
	}
	if launch.command == nil || launch.command.Process == nil || launch.child == nil || launch.command.ProcessState != nil {
		return sessionLeaseError("startup lost its actual unreaped child object")
	}
	return errors.Join(launch.child.checkLive(), checkStartupExecutable(launch.child.proc, launch.executable, launch.argv))
}

func (launch *startupLaunchV1) nativePeer() *nativeStartupPeerV1 {
	if launch.unit != nil {
		return launch.unit.peer
	}
	return launch.child
}

func (launch *startupLaunchV1) connect(ctx context.Context) error {
	deadline := time.Now().Add(startWait)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	for {
		if err := launch.check(ctx); err != nil {
			return err
		}
		conn, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "unix", paths.Socket(launch.manifest.ID))
		if err == nil {
			line, err := NewStartupLineV1(conn)
			if err != nil {
				_ = conn.Close()
				return err
			}
			launch.line = line
			creator, err := peerOf(conn)
			if err != nil {
				return err
			}
			if creator.done != nil {
				defer creator.done()
			}
			if creator.pid != launch.nativePeer().pid || creator.uid != os.Getuid() || creator.alive != nil && !creator.alive() {
				return sessionLeaseError("the startup socket creator differs from the original live native helper")
			}
			if err := launch.nativePeer().checkLive(); err != nil {
				return err
			}
			_ = line.SetDeadline(deadline)
			if err := line.WriteFrame(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
				return err
			}
			frame, err := line.ReadFrame()
			if err != nil {
				launch.readFailed = true
				return err
			}
			if err := line.RejectAncillary(); err != nil {
				return err
			}
			if frame.Type != proto.THello || json.Unmarshal(frame.Payload, &launch.hello) != nil || launch.hello.Check(version.Protocol) != nil ||
				launch.hello.Role != "supervisor" || !supply.IsHex64(launch.hello.StartupNonce) {
				return sessionLeaseError("the original native supervisor did not offer its one startup challenge")
			}
			sender, err := line.currentSender()
			if err != nil {
				return err
			}
			if err := launch.nativePeer().checkNativeSender(sender); err != nil {
				return err
			}
			return launch.check(ctx)
		}
		if !noHolderSocket(err) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (launch *startupLaunchV1) waitNativeExit() error {
	if !launch.started {
		return nil
	}
	if native := launch.nativePeer(); native != nil {
		for {
			polls := []unix.PollFd{{Fd: int32(native.pidfd), Events: unix.POLLIN}}
			n, err := unix.Poll(polls, -1)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
				if launch.command != nil {
					_ = launch.command.Wait()
					launch.exited = true
					return nil
				}
				if launch.manager != nil {
					return launch.manager.events.waitOriginalEnd()
				}
				return errors.Join(sessionLeaseError("the original native child has no positive physical exit"), err)
			}
			launch.exited = true
			if launch.command != nil {
				return launch.command.Wait()
			}
			return nil
		}
	}
	if launch.command != nil && launch.command.Process != nil {
		return launch.waitOriginalCommand()
	}
	if launch.manager != nil {
		// An unresolved first manager operation remains in this actual scoped
		// controller. Only its original negative reply or correlated native
		// unit removal can finish it; cancellation is never synthetic exit.
		return launch.manager.events.waitOriginalEnd()
	}
	return sessionLeaseError("the original startup has no retained native lifetime owner")
}

func (launch *startupLaunchV1) Close() error {
	if launch == nil {
		return nil
	}
	launch.once.Do(func() {
		if launch.buildingDone != nil {
			<-launch.buildingDone
		}
		launch.mu.Lock()
		defer launch.mu.Unlock()
		if !launch.ready {
			if launch.line != nil {
				launch.closeErr = errors.Join(launch.closeErr, launch.cancelPending())
			}
			// Keep the original OP above this call until the exact native exit
			// joins. No detached cleanup goroutine or timeout releases it.
			launch.closeErr = errors.Join(launch.closeErr, launch.waitNativeExit())
		}
		if launch.line != nil {
			launch.closeErr = errors.Join(launch.closeErr, launch.line.Close())
		}
		if launch.unit != nil {
			launch.closeErr = errors.Join(launch.closeErr, launch.unit.Close())
		}
		if launch.child != nil {
			launch.closeErr = errors.Join(launch.closeErr, launch.child.Close())
		}
		if launch.manager != nil {
			launch.closeErr = errors.Join(launch.closeErr, launch.manager.Close())
		}
		if launch.command != nil && launch.ready {
			launch.closeErr = errors.Join(launch.closeErr, launch.command.Process.Release())
		}
		if launch.placement != nil {
			launch.closeErr = errors.Join(launch.closeErr, launch.placement.close())
		}
	})
	return launch.closeErr
}

func (launch *startupLaunchV1) waitOriginalCommand() error {
	if launch.command == nil || launch.command.Process == nil {
		return sessionLeaseError("cleanup lost its original unreaped child object")
	}
	failure := launch.command.Wait()
	if launch.command.ProcessState != nil {
		launch.exited = true
		return failure
	}
	// A failed native wait is not physical exit. Keep this exact creation
	// object and the parent OP in the same scoped owner until a real wait
	// supplies an exit state. Never renew it from a saved numeric PID.
	for {
		state, err := launch.command.Process.Wait()
		if state != nil {
			launch.command.ProcessState = state
			launch.exited = true
			return errors.Join(failure, err)
		}
		if failure == nil {
			failure = err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
