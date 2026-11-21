//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"
)

// The fixed reader owns this exact specification before REAL Start. No
// callback or incoming descriptor data can choose another command or image.
type ordinaryGitStartV2 struct {
	owner    *joinedOrdinaryGitInspectionV2
	command  *exec.Cmd
	attrs    *syscall.SysProcAttr
	args     []string
	env      []string
	code     *os.File
	parent   *os.File
	image    *purgeAttemptImageV2
	codeInfo os.FileInfo
}

func newOrdinaryGitStartV2(owner *joinedOrdinaryGitInspectionV2) (*ordinaryGitStartV2, error) {
	if owner == nil || owner.code == nil || owner.endpoint == nil || owner.preparation == nil || owner.preparation.owner == nil || owner.command != nil || owner.waitDone != nil || owner.creationFD != -1 {
		return nil, sessionLeaseError("the fixed Git start has no fresh original native producer")
	}
	info, err := owner.code.Stat()
	if err != nil || !os.SameFile(info, owner.preparation.owner.origin.executableRef) {
		return nil, errors.Join(sessionLeaseError("the fixed Git start lost its original mapped helper code"), err)
	}
	environment := append(supervisorLaunchEnv(""), ordinaryGitSocketEnvV2+"="+owner.endpoint.path)
	command := exec.Command("/proc/self/fd/3", "git-op", gitOpPurgeInspectV1)
	command.Dir, command.Env, command.ExtraFiles = "/", slices.Clone(environment), []*os.File{owner.code}
	if owner.purgeImage != nil {
		if err := owner.purgeImage.Check(context.Background()); err != nil {
			return nil, err
		}
		fd, err := unix.PidfdOpen(os.Getpid(), 0)
		if err != nil {
			return nil, err
		}
		owner.parent = os.NewFile(uintptr(fd), "actual-fixed-git-fork-parent")
		command.Args = []string{"/proc/self/fd/3", "purge-git-worker-v3"}
		command.ExtraFiles = append(command.ExtraFiles, owner.parent)
	}
	attrs := &syscall.SysProcAttr{PidFD: &owner.creationFD}
	command.SysProcAttr = attrs
	return &ordinaryGitStartV2{owner: owner, command: command, attrs: attrs, args: slices.Clone(command.Args), env: slices.Clone(environment), code: owner.code, codeInfo: info, parent: owner.parent, image: owner.purgeImage}, nil
}

func (start *ordinaryGitStartV2) checkFrozenV2(ctx context.Context) error {
	if start == nil || ctx == nil || start.owner == nil || start.owner.start != start || start.owner.command != start.command || start.owner.code != start.code || start.command == nil || start.attrs == nil {
		return sessionLeaseError("the fixed Git start is not its original concrete caller")
	}
	command, owner := start.command, start.owner
	if err := start.checkCodeRolesV3(ctx); err != nil {
		return err
	}
	if command.Path != "/proc/self/fd/3" || !slices.Equal(command.Args, start.args) || command.Dir != "/" || command.Env == nil || !slices.Equal(command.Env, start.env) || command.SysProcAttr != start.attrs || !reflect.DeepEqual(*start.attrs, syscall.SysProcAttr{PidFD: &owner.creationFD}) || command.Stdin != nil || command.Stdout != nil || command.Stderr != nil || command.Cancel != nil || command.WaitDelay != 0 {
		return sessionLeaseError("the fixed Git actual start changed its immutable command or native roles")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := start.code.Stat()
	if err != nil || !os.SameFile(info, start.codeInfo) || info.Mode() != start.codeInfo.Mode() || info.Size() != start.codeInfo.Size() || !info.ModTime().Equal(start.codeInfo.ModTime()) {
		return errors.Join(sessionLeaseError("the original fixed helper code descriptor changed"), err)
	}
	return nil
}

func (start *ordinaryGitStartV2) startActualV2(ctx context.Context) error {
	if start == nil || start.owner == nil {
		return sessionLeaseError("the actual fixed Git start is absent")
	}
	owner := start.owner
	if owner.command.Process != nil || owner.waitDone != nil || owner.creationFD != -1 {
		return sessionLeaseError("the SAME actual fixed Git Cmd already crossed Start")
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
	if owner.creationFD >= 0 {
		owner.creation = os.NewFile(uintptr(owner.creationFD), "actual-original-fixed-git-helper-pidfd")
	}
	owner.waitDone = make(chan struct{})
	go func() { owner.waitErr = start.command.Wait(); close(owner.waitDone) }()
	return start.checkFrozenV2(ctx)
}
