//go:build linux

package none

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// JoinOwnerV1 is issued from the actual original live init. Its private
// duplicate retains the same atomic creation pidfd through physical cleanup.
// No description, saved process number or caller-supplied file constructs it.
type JoinOwnerV1 struct {
	mu         sync.Mutex
	runtime    *sandbox
	command    *exec.Cmd
	connection net.Conn
	process    *os.File
	closed     bool
}

func (owner *LiveOwnerV1) PrepareJoinV1(ctx context.Context) (*JoinOwnerV1, error) {
	if err := owner.Check(ctx); err != nil {
		return nil, err
	}
	s := owner.runtime
	for !s.life.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.life.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.closed || s.cmd == nil || s.connection != owner.connection || s.pidfd < 0 {
		return nil, failure("the original unwalled creator changed before its completion loan", nil)
	}
	if err := checkUnwalledHandle(s.pidfd); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(uintptr(s.pidfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &JoinOwnerV1{runtime: s, command: s.cmd, connection: s.connection, process: os.NewFile(uintptr(fd), "original-none-init-join")}, nil
}

func (owner *JoinOwnerV1) CheckJoinedV1(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return failure("the original unwalled completion owner is absent", nil)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.runtime == nil || owner.command == nil || owner.process == nil {
		return failure("the original unwalled completion owner ended", nil)
	}
	s := owner.runtime
	for !s.life.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.life.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != owner.command || s.connection != owner.connection || !s.known || !s.reaped || !s.closed || !s.released || s.forced || !s.status.Exited() || s.status.ExitStatus() != 0 || s.killErr != nil {
		return failure("retain the original unwalled init until its main, terminal and native cleanup are positively joined", s.observeErr)
	}
	polls := []unix.PollFd{{Fd: int32(owner.process.Fd()), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return failure("the original unwalled creation handle does not prove physical exit", err)
	}
	return ctx.Err()
}

func (owner *JoinOwnerV1) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	if owner.process == nil {
		return failure("the original unwalled completion handle is absent", nil)
	}
	err := owner.process.Close()
	if err == nil {
		owner.closed = true
	}
	return errors.Join(err)
}
