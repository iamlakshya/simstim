package supervisor

import (
	"context"
	"errors"
	"syscall"

	"github.com/iamlakshya/simstim/internal/host"
)

// boot waits for the session's secrets, then starts the memory watch and
// the boot's deadline, and builds and starts the sandbox and the session
// in it (startGuest). A guest that stalls the start past bootTimeout loses
// its link. A stop with the host ends each step of the boot at once: the
// wait for the secrets, the sandbox's start and the start's write. No
// guest starts after it, and the boot ends with errStopped.
func (s *Supervisor) boot(sb sandbox) error {
	stop, cancel := context.WithCancelCause(s.lifetimeContext())
	defer cancel(context.Canceled)
	s.mu.Lock()
	s.bootCancel, s.bootContextGeneration = cancel, s.generation
	generation := s.generation
	failedDisk := s.diskFailure
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.bootContextGeneration == generation {
			s.bootCancel = nil
			s.bootContextGeneration = ""
		}
		s.mu.Unlock()
	}()
	if failedDisk != nil {
		cancel(failedDisk)
	}
	done, stopped := make(chan struct{}), make(chan bool, 1)
	go func() {
		select {
		case <-s.sig:
			s.markStopIntent()
			cancel(errStopped)
			stopped <- true
		case <-done:
			stopped <- false
		}
	}()
	err := s.startUp(stop, sb)
	close(done)
	wasStopped := <-stopped
	if cause := context.Cause(stop); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, errStopped) {
		s.raiseDiskFailure()
		return cause
	}
	if wasStopped {
		if err != nil {
			return errStopped
		}
		// The stop came as the boot ended: the run takes it.
		select {
		case s.sig <- syscall.SIGTERM:
		default:
		}
	}
	return err
}

// startUp is the boot's steps, which stop ends.
func (s *Supervisor) startUp(stop context.Context, sb sandbox) error {
	secrets := s.awaitSecrets(stop)
	defer s.closeStartupInput()
	if s.startupErr != nil {
		return s.startupErr
	}
	if stop.Err() != nil {
		return errStopped
	}
	budget := newActiveBootBudget(stop, s.bootLimit())
	defer budget.close()
	s.bootBudget = budget
	budget.publisher = s
	s.mu.Lock()
	s.rec.mem = host.NewUnitWatch(unitDir(s.id))
	s.mu.Unlock()
	if err := s.publishActiveBudget(budget); err != nil {
		return err
	}
	if err := s.prepareDiskWatcherV1(sb, budget); err != nil {
		return err
	}
	return s.startGuest(budget, sb, secrets)
}
