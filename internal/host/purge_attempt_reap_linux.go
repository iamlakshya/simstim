//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const purgeAttemptAdoptionFDV3 = 7

// The actual original Cmd's only Wait reader is held behind this gate before
// REAL Start. Neither a pidfd, exit observation nor request cancellation can
// reserve a numeric PID after reaping; only the unreaped child does so.
type purgeAttemptReapV3 struct {
	mu       sync.Mutex
	owner    *purgeAttemptExecV1
	command  *exec.Cmd
	allow    chan struct{}
	released bool
}

func newPurgeAttemptReapV3(owner *purgeAttemptExecV1, command *exec.Cmd) (*purgeAttemptReapV3, error) {
	if owner == nil || command == nil || owner.process != nil || command.Process != nil || owner.waitDone != nil || owner.reap != nil {
		return nil, placementError("the purge reaping hold has no original unstarted Cmd")
	}
	return &purgeAttemptReapV3{owner: owner, command: command, allow: make(chan struct{})}, nil
}

func (hold *purgeAttemptReapV3) beginOriginalWaitV3() error {
	if hold == nil || hold.owner == nil || hold.command == nil || hold.owner.reap != hold || hold.owner.command != hold.command || hold.owner.process == nil || hold.command.Process != hold.owner.process || hold.owner.waitDone != nil {
		return placementError("the reaping hold lost its original actual Start")
	}
	owner := hold.owner
	owner.waitDone = make(chan struct{})
	go func() {
		<-hold.allow
		owner.waitErr = hold.command.Wait()
		close(owner.waitDone)
	}()
	return nil
}

func (hold *purgeAttemptReapV3) checkHeldV3(ctx context.Context) error {
	if hold == nil || ctx == nil || hold.owner == nil || hold.owner.reap != hold || hold.command == nil || hold.owner.command != hold.command || hold.owner.process == nil || hold.command.Process != hold.owner.process || hold.owner.creation == nil || hold.owner.creationFD < 0 || int(hold.owner.creation.Fd()) != hold.owner.creationFD || hold.owner.waitDone == nil {
		return placementError("the original numeric adoption has no actual unreaped child")
	}
	hold.mu.Lock()
	defer hold.mu.Unlock()
	if hold.released {
		return placementError("the original child reaping hold has already opened")
	}
	select {
	case <-hold.allow:
		return placementError("the original child Wait gate opened before adoption settled")
	default:
	}
	select {
	case <-hold.owner.waitDone:
		return placementError("the original child was reaped before adoption settled")
	default:
	}
	if hold.command.ProcessState != nil {
		return placementError("the original Cmd already has a reaped native result")
	}
	if err := checkPurgeOriginalPidfdV3(hold.owner); err != nil {
		return err
	}
	// WNOWAIT observes the SAME child without reaping it. ECHILD also refuses
	// a foreign/reaped PIDFD, even when a stale pidfd still reports exit.
	var status unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PIDFD, int(hold.owner.creation.Fd()), &status, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return errors.Join(placementError("the original creation is no longer an unreaped child of this parent"), err)
		}
		break
	}
	return ctx.Err()
}

func (hold *purgeAttemptReapV3) releaseAfterScopeV3(scope *purgeAttemptScopeV3) error {
	if hold == nil || scope == nil || hold.owner == nil || hold.owner.scope != scope || scope.owner != hold.owner || hold.owner.reap != hold {
		return placementError("the actual manager settlement has another original reaping hold")
	}
	if err := scope.checkTerminalForReapV3(); err != nil {
		return err
	}
	if err := hold.checkHeldV3(context.Background()); err != nil {
		return err
	}
	hold.mu.Lock()
	defer hold.mu.Unlock()
	if !hold.released {
		hold.released = true
		close(hold.allow)
	}
	return nil
}

// This PUBLIC latch delays bootstrap exit only. It cannot admit code, install
// a domain, construct an OP or establish manager custody. Unknown adoption
// survives parent death/EOF rather than voluntarily freeing its numeric PID.
func waitPurgeAttemptAdoptionV3(file *os.File) error {
	for {
		if err := ordinaryJoinLatchV1(file, true); err == nil {
			return nil
		}
		if err := ordinaryJoinLatchV1(file, false); err != nil {
			return err
		}
		polls := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(polls, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n != 1 || polls[0].Revents != unix.POLLIN {
			return placementError("the original adoption latch lost its native lifetime")
		}
	}
}

func checkPurgeOriginalPidfdV3(owner *purgeAttemptExecV1) error {
	if owner == nil || owner.creation == nil || owner.process == nil || owner.creationFD < 0 || int(owner.creation.Fd()) != owner.creationFD {
		return placementError("the original creation has no same retained atomic pidfd")
	}
	raw, err := ordinaryFDInfoV1(owner.creation)
	if err != nil {
		return err
	}
	pid, seen := 0, false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "Pid:" {
			continue
		}
		if seen {
			return placementError("the original creation pidfd has duplicate native PID observations")
		}
		pid, err = strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			return placementError("the original creation pidfd no longer names its unreaped process")
		}
		seen = true
	}
	if !seen || pid != owner.process.Pid {
		return placementError("the native reaping hold has another original process")
	}
	return nil
}
