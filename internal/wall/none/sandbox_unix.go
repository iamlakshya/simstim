//go:build linux || darwin

package none

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/fsbirth"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/wall"
)

type sandbox struct {
	spec                             wall.Spec
	mu                               sync.Mutex
	life                             sync.Mutex
	root                             *os.Root
	rootRef                          cachefs.Reference
	runtimeParent, runtime           *os.Root
	runtimeRef                       cachefs.Reference
	runtimeParentRef                 cachefs.Reference
	runtimeBirth, runtimeParentBirth *fsbirth.DataV2
	created                          os.FileInfo
	cmd                              *exec.Cmd
	connection                       net.Conn
	pidfd                            int
	observed                         chan struct{}
	status                           syscall.WaitStatus
	known                            bool
	observeErr                       error
	killErr                          error
	started, closed, reaped, forced  bool
	released                         bool
	handed                           bool
	live                             *LiveOwnerV1
	result                           wall.Exit
}

func newSandbox(ctx context.Context, spec wall.Spec) (_ wall.Sandbox, returned error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return nil, failure("cannot pin the unwalled session folder", err)
	}
	ref, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	s := &sandbox{spec: spec, root: root, rootRef: ref, pidfd: -1, observed: make(chan struct{})}
	return s, nil
}

func (s *sandbox) Start(ctx context.Context) (_ net.Conn, returned error) {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	unavailable := s.started || s.closed
	s.mu.Unlock()
	if unavailable {
		return nil, failure("this original unwalled runtime was already started or closed", nil)
	}
	if err := errors.Join(ctx.Err(), s.rootRef.CheckRoot(s.root)); err != nil {
		return nil, err
	}
	if err := s.prepareRuntime(ctx); err != nil {
		return nil, errors.Join(err, s.releaseRuntime())
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, s.releaseRuntime())
		}
	}()
	fd, err := unwalledSocketPairV1()
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(fd[0]), "unwalled-supervisor-link")
	child := os.NewFile(uintptr(fd[1]), "unwalled-guest-link")
	defer child.Close()
	connection, err := net.FileConn(parent)
	closeErr := parent.Close()
	if err != nil || closeErr != nil {
		if connection != nil {
			connection.Close()
		}
		return nil, errors.Join(err, closeErr)
	}
	command, helper, err := unwalledCommandV1(child, s.spec.ID)
	if err != nil {
		connection.Close()
		return nil, err
	}
	defer helper.Close()
	log, err := cachefs.OpenFile(s.root, "guest-init.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		connection.Close()
		return nil, err
	}
	defer log.Close()
	command.Env = os.Environ()
	command.Stdout, command.Stderr = log, log
	command.WaitDelay = time.Second
	command.SysProcAttr = unwalledAttributes(&s.pidfd)
	if err := errors.Join(ctx.Err(), s.rootRef.CheckRoot(s.root), s.runtimeRef.CheckRoot(s.runtime)); err != nil {
		connection.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		connection.Close()
		return nil, failure("cannot start the unwalled guest init", err)
	}
	s.mu.Lock()
	s.cmd, s.connection, s.started = command, connection, true
	s.mu.Unlock()
	go func() {
		status, known, err := proc.AwaitStatusError(command.Process.Pid)
		s.mu.Lock()
		s.status, s.known, s.observeErr = status, known, err
		s.mu.Unlock()
		close(s.observed)
	}()
	complete = true // from here this actual runtime owns every partial child
	checked, err := link.ReadGuestHello(ctx, connection)
	if err != nil {
		connection.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		connection.Close()
		return nil, err
	}
	s.mu.Lock()
	s.connection = checked
	s.handed = true
	s.mu.Unlock()
	return checked, nil
}

func (s *sandbox) Stop(ctx context.Context, grace time.Duration) error {
	s.mu.Lock()
	command := s.cmd
	s.mu.Unlock()
	if command == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(max(grace, 0))
	defer timer.Stop()
	select {
	case <-s.observed:
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Kill()
}

func (s *sandbox) Kill() (returned error) {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	command, closed, prior := s.cmd, s.closed, s.killErr
	s.mu.Unlock()
	if closed {
		return prior
	}
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.killErr = errors.Join(s.killErr, returned)
		returned = s.killErr
	}()
	if command == nil {
		err := s.releaseRuntime()
		if err == nil {
			s.mu.Lock()
			s.closed = true
			s.released = true
			s.mu.Unlock()
		}
		return err
	}
	pid := command.Process.Pid // retained original unreaped child, never a saved PID
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = command.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.observed:
	case <-time.After(10 * time.Second):
		s.mu.Lock()
		s.forced = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	known := s.known
	observeErr := s.observeErr
	s.mu.Unlock()
	if !known {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = command.Process.Kill()
		select {
		case <-s.observed:
		case <-time.After(5 * time.Second):
			return failure("retain the original unwalled process until its physical exit is proved", nil)
		}
		s.mu.Lock()
		known, observeErr = s.known, s.observeErr
		s.mu.Unlock()
	}
	if !known {
		return failure("the original unwalled child has no positive native exit observation", observeErr)
	}
	// Signal its group for the last time BEFORE reaping this original leader.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	err := command.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		err = nil
	} // the trusted exit status is projected separately
	s.mu.Lock()
	s.reaped, s.closed = true, true
	connection := s.connection
	s.mu.Unlock()
	if connection != nil {
		closeErr := connection.Close()
		if !errors.Is(closeErr, net.ErrClosed) && !errors.Is(closeErr, os.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	handed := s.handed
	s.mu.Unlock()
	if handed && !s.CleanExit() {
		return errors.Join(err, failure("retain the original unwalled runtime after an unconfirmed init or terminal cleanup", nil))
	}
	if keepErr := s.keepHolderLogs(); keepErr != nil {
		return errors.Join(err, keepErr)
	}
	if closeErr := closeUnwalledHandle(s.pidfd); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if releaseErr := s.releaseRuntime(); releaseErr != nil {
		return errors.Join(err, releaseErr)
	}
	s.mu.Lock()
	s.released = true
	s.mu.Unlock()
	return err
}

func (s *sandbox) Wait() wall.Exit {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return wall.Exit{Code: -1, Err: failure("the unwalled runtime did not start", nil)}
	}
	<-s.observed
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known {
		return wall.Exit{Code: -1, Err: failure("the original unwalled child has no native exit status", s.observeErr)}
	}
	result := wall.Exit{Code: -1}
	if s.status.Exited() {
		result.Code = s.status.ExitStatus()
	} else if s.status.Signaled() {
		result.Signal = s.status.Signal().String()
	}
	return result
}
func (s *sandbox) CleanExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.known && s.status.Exited() && s.status.ExitStatus() == 0 && !s.forced
}
func (s *sandbox) PhysicalExitV1() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reaped && s.closed
}

func (s *sandbox) CleanupCompleteV1() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed && s.released && s.started && s.handed && s.reaped && s.known && !s.forced && s.status.Exited() && s.status.ExitStatus() == 0 && s.killErr == nil
}

// PreparedCleanupCompleteV1 covers only a factory which never handed any
// guest link to a controller. It cannot issue N5 stopped-run confirmation.
func (s *sandbox) PreparedCleanupCompleteV1() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed && s.released && !s.handed && s.killErr == nil && (!s.started || s.reaped && s.known)
}

// CleanupPIDV1 returns diagnostic DATA only after this SAME creator has joined
// its original command, terminal and runtime resources. It supplies no handle.
func (owner *LiveOwnerV1) CleanupObservationV1() (int, string, error) {
	if owner == nil || owner.runtime == nil {
		return 0, "", failure("the original unwalled cleanup owner is absent", nil)
	}
	s := owner.runtime
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != owner || s.cmd == nil || s.connection != owner.connection || !s.started || !s.handed || !s.closed || !s.released || !s.reaped || !s.known || s.forced || !s.status.Exited() || s.status.ExitStatus() != 0 || s.killErr != nil {
		return 0, "", failure("the original unwalled creator has not completed its physical cleanup", s.observeErr)
	}
	return s.cmd.Process.Pid, owner.birth, nil
}
func (s *sandbox) Abilities() wall.Abilities { return wall.Abilities{} }
func (s *sandbox) Destroy() error            { return s.Kill() }
func (s *sandbox) ReleasePreparedV1() error {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		return failure("a running unwalled runtime cannot release prepared resources", nil)
	}
	err := s.releaseRuntime()
	if err == nil {
		s.mu.Lock()
		s.closed = true
		s.released = true
		s.mu.Unlock()
	}
	return err
}

func (s *sandbox) releaseRuntime() (returned error) {
	if s.runtime != nil {
		if err := s.runtimeRef.CheckRoot(s.runtime); err != nil {
			return err
		}
		reader, err := s.runtime.Open(".")
		if err != nil {
			return err
		}
		for {
			entries, err := reader.ReadDir(64)
			for _, entry := range entries {
				returned = errors.Join(returned, s.runtime.RemoveAll(entry.Name()))
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				returned = errors.Join(returned, err)
				break
			}
		}
		returned = errors.Join(returned, reader.Close())
		if returned != nil {
			return returned
		}
		current, err := s.runtimeParent.Lstat(s.spec.ID)
		if err != nil || s.created == nil || !os.SameFile(s.created, current) {
			return errors.Join(failure("the original unwalled runtime folder changed before cleanup", nil), err)
		}
		returned = errors.Join(returned, s.runtime.Close(), s.runtimeParent.Remove(s.spec.ID))
		s.runtime = nil
	}
	if s.runtimeParent != nil {
		returned = errors.Join(returned, s.runtimeParent.Close())
		s.runtimeParent = nil
	}
	if s.root != nil {
		returned = errors.Join(returned, s.root.Close())
		s.root = nil
	}
	return returned
}

func (s *sandbox) keepHolderLogs() (returned error) {
	if s.runtime == nil || s.root == nil {
		return nil
	}
	members, err := cachefs.OpenPrivateChild(s.runtime, "members", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer members.Close()
	reader, err := members.Open(".")
	if err != nil {
		return err
	}
	defer reader.Close()
	for {
		entries, err := reader.ReadDir(64)
		for _, entry := range entries {
			if !api.ValidSessionID(entry.Name()) {
				continue
			}
			member, err := cachefs.OpenPrivateChild(members, entry.Name(), false)
			if err != nil {
				return err
			}
			log, err := cachefs.OpenFile(member, "holder.log", os.O_RDONLY, 0600)
			if errors.Is(err, os.ErrNotExist) {
				member.Close()
				continue
			}
			if err != nil {
				member.Close()
				return err
			}
			info, err := log.Stat()
			if err == nil && info.Size() > 1<<20 {
				_, err = log.Seek(info.Size()-(1<<20), io.SeekStart)
			}
			if err != nil {
				log.Close()
				member.Close()
				return err
			}
			if current, err := s.root.Lstat("guest-holder.log"); err == nil && current.Size() >= 1<<20 {
				if err := s.root.Rename("guest-holder.log", "guest-holder.log.1"); err != nil {
					log.Close()
					member.Close()
					return err
				}
			}
			output, err := cachefs.OpenFile(s.root, "guest-holder.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
			if err != nil {
				log.Close()
				member.Close()
				return err
			}
			_, copyErr := io.Copy(output, io.LimitReader(log, 1<<20))
			returned = errors.Join(returned, copyErr, output.Close(), log.Close(), member.Close())
		}
		if errors.Is(err, io.EOF) {
			return returned
		}
		if err != nil {
			return errors.Join(returned, err)
		}
	}
}
