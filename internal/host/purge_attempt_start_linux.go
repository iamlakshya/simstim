//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"
)

// Only this real code-factory-backed producer creates a start specification.
// It owns the actual Cmd and exact inherited native objects around REAL Start.
// There is no command, argv, environment or expected-file setter.
type purgeAttemptStartV2 struct {
	creator *purgeAttemptExecV1
	command *exec.Cmd
	attrs   *syscall.SysProcAttr
	argv    []string
	env     []string
	dir     string
	files   []*os.File
}

func newPurgeAttemptStartV2(creator *purgeAttemptExecV1) (*purgeAttemptStartV2, error) {
	if creator == nil || creator.code == nil || creator.loan == nil || creator.loan.creator != creator.code || creator.command != nil || creator.process != nil {
		return nil, placementError("the closed purge start has no original unstarted code-factory execution owner")
	}
	dir := filepath.Join(creator.code.home.Name(), creator.code.directoryLeaf)
	environment := slices.Clone(supervisorLaunchEnv(""))
	files := []*os.File{creator.loan.OriginalCode, creator.loan.CopiedCode, creator.loan.ParentPidfd, creator.loan.Cancel}
	for _, file := range files {
		if file == nil {
			return nil, placementError("the original closed start lost a native bootstrap role")
		}
	}
	command := exec.Command("/proc/self/fd/4", "purge-attempt-v2")
	command.Dir, command.Env, command.ExtraFiles = dir, slices.Clone(environment), slices.Clone(files)
	attrs := &syscall.SysProcAttr{Setsid: true, PidFD: &creator.creationFD}
	command.SysProcAttr = attrs
	return &purgeAttemptStartV2{creator: creator, command: command, attrs: attrs, argv: slices.Clone(command.Args), env: environment, dir: dir, files: files}, nil
}

func (start *purgeAttemptStartV2) checkRetainedSpecV3(ctx context.Context) error {
	if start == nil || ctx == nil || start.creator == nil || start.creator.code == nil || start.creator.loan == nil || start.command == nil || start.attrs == nil {
		return placementError("the actual closed purge start is absent")
	}
	owner, command := start.creator, start.command
	if owner.start != start || owner.command != command || command.Path != "/proc/self/fd/4" || !slices.Equal(command.Args, start.argv) || len(start.argv) != 2 || start.argv[0] != "/proc/self/fd/4" || start.argv[1] != "purge-attempt-v2" || command.Dir != start.dir || command.Env == nil || !slices.Equal(command.Env, start.env) || !slices.Equal(command.ExtraFiles, start.files) || command.SysProcAttr != start.attrs || !reflect.DeepEqual(*start.attrs, syscall.SysProcAttr{Setsid: true, PidFD: &owner.creationFD}) || command.Stdin != nil || command.Stdout != nil || command.Stderr != nil || command.Cancel != nil || command.WaitDelay != 0 {
		return placementError("the original closed purge command changed before or after its actual Start")
	}
	if len(start.files) != 4 || start.files[0] != owner.loan.OriginalCode || start.files[1] != owner.loan.CopiedCode || start.files[2] != owner.loan.ParentPidfd || start.files[3] != owner.loan.Cancel {
		return placementError("the actual purge start changed its original native bootstrap objects")
	}
	return ctx.Err()
}

func (start *purgeAttemptStartV2) checkFrozenV2(ctx context.Context) error {
	if err := start.checkRetainedSpecV3(ctx); err != nil {
		return err
	}
	owner := start.creator
	if err := owner.code.checkForOriginalExecutionV2(ctx, owner); err != nil {
		return err
	}
	if err := checkPurgeKernelExecEligibilityV2(ctx, owner.loan.CopiedCode); err != nil {
		return err
	}
	copyInfo, err := owner.loan.CopiedCode.Stat()
	if err != nil {
		return err
	}
	originalCopy, err := owner.code.copy.Stat()
	if err != nil || !os.SameFile(copyInfo, originalCopy) {
		return errors.Join(placementError("the exact held FD4 differs from its original code-factory inode"), err)
	}
	return nil
}

func (start *purgeAttemptStartV2) startActualV2(ctx context.Context) error {
	if start == nil || start.creator == nil {
		return placementError("the actual closed start is absent")
	}
	owner := start.creator
	if owner.command != start.command || owner.process != nil || start.command.Process != nil || owner.waitDone != nil || owner.creationFD != -1 {
		return placementError("the same actual purge Cmd has already crossed its original Start boundary")
	}
	if err := start.checkFrozenV2(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := start.command.Start(); err != nil {
		return err
	}
	owner.process = start.command.Process
	if owner.creationFD >= 0 {
		owner.creation = os.NewFile(uintptr(owner.creationFD), "actual-atomic-purge-creation-pidfd")
	}
	owner.waitDone = make(chan struct{})
	go func() { owner.waitErr = start.command.Wait(); close(owner.waitDone) }()
	return start.checkFrozenV2(ctx)
}

func (owner *purgeAttemptExecV1) checkJoinedCreationV2() error {
	if owner == nil || owner.start == nil || owner.command == nil || owner.process == nil || owner.command.Process != owner.process || owner.creation == nil || owner.creationFD < 0 || int(owner.creation.Fd()) != owner.creationFD || owner.waitDone == nil {
		return placementError("the original purge creation has no retained native join source")
	}
	select {
	case <-owner.waitDone:
	default:
		return placementError("the actual original purge Cmd.Wait has not joined")
	}
	state := owner.command.ProcessState
	if state == nil || state.Pid() != owner.process.Pid {
		return placementError("the actual original process has no positive native wait result")
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Exited() && !status.Signaled() {
		return placementError("the actual original process wait remains unknown")
	}
	if owner.waitErr != nil {
		var exited *exec.ExitError
		if !errors.As(owner.waitErr, &exited) || exited.ProcessState != state {
			return errors.Join(placementError("the original purge Wait returned an unknown error"), owner.waitErr)
		}
	}
	// This is the exact handle returned by the REAL Start above. A closed
	// goroutine channel, wait error or missing handle supplies no exit fact.
	if err := unix.PidfdSendSignal(int(owner.creation.Fd()), 0, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	polls := []unix.PollFd{{Fd: int32(owner.creation.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(placementError("the SAME original purge creation has no positive kernel exit"), err)
	}
	return nil
}

// bindOriginalExecutionV2 binds this actual factory to one original creator
// object before REAL Start. It cannot select a replacement Cmd after an error
// or use a phase/EOF to create another execution from the retained code.
func (code *purgeAttemptCodeV2) bindOriginalExecutionV2(ctx context.Context, owner *purgeAttemptExecV1) error {
	if code == nil || owner == nil || owner.code != code || owner.command != nil || owner.process != nil || owner.waitDone != nil {
		return placementError("the original code factory has no fresh concrete execution owner")
	}
	code.mu.Lock()
	defer code.mu.Unlock()
	if err := code.checkLockedV2(ctx); err != nil {
		return err
	}
	if code.execution != nil {
		return placementError("the same actual code factory already has its original execution issuer")
	}
	code.execution = owner
	return nil
}

func (code *purgeAttemptCodeV2) checkForOriginalExecutionV2(ctx context.Context, owner *purgeAttemptExecV1) error {
	if code == nil || owner == nil {
		return placementError("the actual execution has no code factory")
	}
	code.mu.Lock()
	defer code.mu.Unlock()
	if code.execution != owner || owner.code != code {
		return placementError("the code factory no longer has this SAME original execution issuer")
	}
	return code.checkLockedV2(ctx)
}
