//go:build linux

package microvm

import (
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/wall"
)

// ownedVMProcess is implemented in production by the original jail.Cmd.
// It exposes no recovered PID or replacement process lookup.
type ownedVMProcess interface {
	Wait() error
	Signal(os.Signal) error
	PhysicalExitV1() bool
}

func (s *sandbox) waitProcess(command ownedVMProcess, control *runnerControl) {
	s.reapMu.Lock()
	defer s.reapMu.Unlock()
	err := command.Wait()
	if !command.PhysicalExitV1() {
		// The diagnostic writer can still be alive. Do not join its EOF or
		// release any disk/runtime lease after a failed native wait.
		if err == nil {
			err = fmt.Errorf("the owned microvm has no physical exit proof")
		}
		s.mu.Lock()
		s.exit.Code = -1
		s.exit.Err = errors.Join(s.exit.Err, err)
		connection := s.connection
		s.closeDoneLocked()
		s.mu.Unlock()
		if connection != nil {
			connection.Close()
		}
		return
	}
	s.finishPhysicalExit(control, err)
}

// refreshPhysicalExit polls only the original retained owner. An active Wait
// keeps the reaping mutex, so this method never starts a second blocking wait.
// A later call may finish cleanup after an earlier uncertain result.
func (s *sandbox) refreshPhysicalExit() bool {
	s.mu.Lock()
	if s.physicalExit {
		s.mu.Unlock()
		return true
	}
	command, control := s.command, s.control
	s.mu.Unlock()
	if command == nil || !s.reapMu.TryLock() {
		return false
	}
	defer s.reapMu.Unlock()
	s.mu.Lock()
	complete := s.physicalExit
	s.mu.Unlock()
	if complete {
		return true
	}
	if !command.PhysicalExitV1() {
		return false
	}
	s.finishPhysicalExit(control, nil)
	return true
}

// finishPhysicalExit requires positive native proof and the reaping mutex.
// Earlier uncertainty remains an error even when cleanup later succeeds.
func (s *sandbox) finishPhysicalExit(control *runnerControl, waitErr error) {
	if s.logDone != nil {
		<-s.logDone
	}
	exit := wall.Exit{Code: -1}
	if control != nil {
		control.finish()
		exit = control.processExit()
	}
	s.mu.Lock()
	previous := s.exit.Err
	s.mu.Unlock()
	exit.Err = errors.Join(exit.Err, previous, waitErr)
	if exit.Code == 0 && exit.Err != nil {
		exit.Code = -1
	}
	if logErr := s.recordDiagnostics(); logErr != nil {
		exit.Err = errors.Join(exit.Err, fmt.Errorf("record bounded VMM diagnostics: %w", logErr))
	}
	if exit.Err != nil {
		if line := s.log.lastLine(); line != "" {
			exit.Err = fmt.Errorf("%w; VMM: %s", exit.Err, line)
		}
	}
	snapshotErr := s.closeSnapshotDiskV1()
	exit.Err = errors.Join(exit.Err, snapshotErr)
	if snapshotErr == nil {
		exit.Err = errors.Join(exit.Err, s.releaseBootStackV2())
	}
	s.mu.Lock()
	s.exit, s.physicalExit = exit, true
	connection := s.connection
	s.closeDoneLocked()
	s.mu.Unlock()
	if connection != nil {
		connection.Close()
	}
}

func (s *sandbox) closeDoneLocked() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// CleanExit is owned-process evidence for the canonical state publisher.
// A guest status claim, forced stop or incomplete start cannot establish it.
func (s *sandbox) CleanExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
	default:
		return false
	}
	return s.cleanExitLocked()
}

// cleanExitLocked is the ONE owned natural-exit predicate. Caller holds mu
// and has already observed done; neither guest claims nor Stop can satisfy it.
func (s *sandbox) cleanExitLocked() bool {
	return s.started && s.physicalExit && !s.forced && s.exit.Code == 0 && s.exit.Signal == "" && s.exit.Err == nil
}
