//go:build linux

package process

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

func (s *sandbox) reap(cmd *exec.Cmd) {
	err := cmd.Wait()
	// The final ownership message may already be queued when the launcher
	// exits. Join its reader before deciding that there is no owned init.
	if s.transferDone != nil {
		<-s.transferDone
	}
	s.mu.Lock()
	var cleanupErr error
	if s.pidfd >= 0 {
		ready, readinessErr := pollInitExit(s.pidfd)
		cleanupErr = errors.Join(cleanupErr, readinessErr)
		var signalErr error
		if !ready {
			s.forced = true
			signalErr = signalInit(s.pidfd, unix.SIGKILL, nil, 0)
		}
		if signalErr != nil && !errors.Is(signalErr, unix.ESRCH) {
			// A failed kill cannot make a live init ready. Retain ownership
			// and return uncertainty rather than block forever on that init.
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("end owned namespace init: %w", signalErr))
		} else if waitErr := waitInitExit(s.pidfd); waitErr != nil {
			// Keep this exact owned descriptor. A completed launcher is not
			// proof that its namespace init and descendants have ceased.
			cleanupErr = errors.Join(cleanupErr, waitErr)
			s.forced = true
		} else {
			s.initProven = true
			cleanupErr = errors.Join(cleanupErr, unix.Close(s.pidfd))
			s.pidfd = -1
		}
	}
	s.result = wall.Exit{Err: errors.Join(s.result.Err, err, cleanupErr)}
	if cmd.ProcessState != nil {
		s.result.Code = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			s.result.Signal = ws.Signal().String()
		}
	}
	copyOwner := s.copy
	creator, freezer := s.creator, s.freezer
	canReleaseCopy := s.pidfd < 0
	s.mu.Unlock()
	creatorProven, creatorErr := creator.check()
	canReleaseCopy = canReleaseCopy && creatorProven
	var copyErr error
	if copyOwner != nil {
		if canReleaseCopy {
			copyErr = copyOwner.native.nativeExit()
		} else {
			copyOwner.native.interrupt()
			copyErr = setupError("preserve copy resources until original init exit is proven", nil)
		}
	}
	var freezeErr error
	if freezer != nil && canReleaseCopy && copyErr == nil {
		freezeErr = freezer.closeEmpty()
	}
	if creatorProven && copyErr == nil && freezeErr == nil {
		creatorErr = errors.Join(creatorErr, creator.close())
	}
	s.mu.Lock()
	s.result.Err = errors.Join(s.result.Err, copyErr, creatorErr, freezeErr)
	s.closed = s.pidfd < 0 && copyErr == nil && creatorProven && freezeErr == nil
	close(s.exited)
	s.mu.Unlock()
}
