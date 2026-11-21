//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Stop terminates the namespace init after the caller's graceful ctl exchange.
func (s *sandbox) Stop(ctx context.Context, grace time.Duration) error {
	thawErr := s.ThawV1()
	s.mu.Lock()
	signalErr := thawErr
	if s.pidfd >= 0 {
		if err := signalInit(s.pidfd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			signalErr = errors.Join(signalErr, err)
		}
	}
	started := s.cmd != nil
	s.mu.Unlock()
	if !started {
		return thawErr
	}
	timer := time.NewTimer(max(grace, 0))
	defer timer.Stop()
	select {
	case <-s.exited:
		s.mu.Lock()
		defer s.mu.Unlock()
		return errors.Join(signalErr, s.result.Err)
	case <-ctx.Done():
		return errors.Join(ctx.Err(), signalErr, s.Kill())
	case <-timer.C:
		return errors.Join(signalErr, s.Kill())
	}
}

// Kill ends the whole PID namespace through its retained init pidfd.
func (s *sandbox) Kill() error {
	thawErr := s.ThawV1()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return thawErr
	}
	s.result.Err = errors.Join(s.result.Err, thawErr)
	if s.pidfd >= 0 {
		ready, readinessErr := pollInitExit(s.pidfd)
		s.result.Err = errors.Join(s.result.Err, readinessErr)
		if !ready {
			s.forced = true
		}
		if err := signalInit(s.pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			s.result.Err = errors.Join(s.result.Err, fmt.Errorf("kill namespace init: %w", err))
			return s.result.Err
		}
		select {
		case <-s.exited:
			// A previous wait error retained this descriptor. Retry physical
			// cleanup against the same owned init, preserving the old error.
			if err := waitInitExit(s.pidfd); err != nil {
				return errors.Join(s.result.Err, err)
			}
			s.initProven = true
			closeErr := unix.Close(s.pidfd)
			s.pidfd, s.closed = -1, true
			s.result.Err = errors.Join(s.result.Err, closeErr)
		default:
		}
	}
	if s.copy != nil {
		s.copy.native.interrupt()
	}
	// Before the acknowledged transfer the outer launcher owns the blocked
	// child. Closing its link refuses activation and its own cleanup kills it.
	if s.link != nil {
		_ = s.link.Close()
	}
	if s.cmd != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	} else {
		s.closed = true
	}
	return s.result.Err
}
