//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/proc"
	"golang.org/x/sys/unix"
)

// quiesceOwnedMainV2 is only a bounded original-main primitive. The actual
// native control handler retains this holder's restoration obligation on
// cancellation and original outside-parent death.
func (h *Holder) quiesceOwnedMainV2(ctx context.Context) (_ *ownedMainPauseV2, returned error) {
	if h == nil || ctx == nil || h.main == nil || h.main.cmd == nil || h.main.cmd != h.cmd || h.cmd.Process == nil || h.main.pidfd < 0 || h.ptmx == nil || h.waitDone == nil {
		return nil, sessionLeaseError("the original main has no supported creation, terminal and wait owner")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	main := h.main
	main.mu.Lock()
	defer main.mu.Unlock()
	if main.purgePause != nil {
		return nil, sessionLeaseError("the original main already retains a reversible pause owner")
	}
	pause := &ownedMainPauseV2{holder: h, main: main}
	main.purgePause = pause // restoration custody exists BEFORE any signal.
	complete := false
	defer func() {
		if !complete {
			// This restores only an existing actual creation. It creates no
			// new task, native role, stop permission or metadata generation.
			returned = errors.Join(returned, pause.restoreLockedV2(context.Background()))
		}
	}()
	if originalMainJoinedV2(h, main) {
		pause.wasExited = true
		complete = true
		return pause, nil
	}
	state, err := observeOriginalMainV2(ctx, h, main)
	if err != nil {
		return pause, err
	}
	switch state {
	case proc.ChildExitedV2:
		pause.wasExited = true
	case proc.ChildStoppedV2:
		pause.wasStopped = true
	default:
		// Only this actual original creation is signalled. Never a group
		// number or a process recovered from a saved record.
		if err := unix.PidfdSendSignal(main.pidfd, unix.SIGSTOP, nil, 0); err != nil {
			return pause, err
		}
		pause.changed = true
		for {
			state, err := observeOriginalMainV2(ctx, h, main)
			if err != nil {
				return pause, err
			}
			if state == proc.ChildStoppedV2 {
				break
			}
			if state == proc.ChildExitedV2 {
				return pause, sessionLeaseError("the original live main ended during reversible preparation")
			}
			if err := waitMainObservationTurnV2(ctx); err != nil {
				return pause, err
			}
		}
	}
	complete = true
	return pause, nil
}

func originalMainJoinedV2(h *Holder, main *holderMainV1) bool {
	select {
	case <-h.waitDone:
	default:
		return false
	}
	polls := []unix.PollFd{{Fd: int32(main.pidfd), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	return err == nil && count == 1 && polls[0].Revents&unix.POLLIN != 0
}

func observeOriginalMainV2(ctx context.Context, h *Holder, main *holderMainV1) (proc.ChildPidfdStateV2, error) {
	for {
		if err := ctx.Err(); err != nil {
			return proc.ChildNoEventV2, err
		}
		if originalMainJoinedV2(h, main) {
			return proc.ChildExitedV2, nil
		}
		state, err := proc.ObserveChildPidfdV2(main.pidfd)
		if errors.Is(err, unix.ECHILD) || state == proc.ChildExitedV2 {
			// Cmd.Wait may have reaped it while its actual holder waiter is
			// still draining/saving. ECHILD alone remains unknown; join the
			// SAME real waiter before accepting this original prior exit.
			if err := waitMainObservationTurnV2(ctx); err != nil {
				return proc.ChildNoEventV2, err
			}
			continue
		}
		return state, err
	}
}

func waitMainObservationTurnV2(ctx context.Context) error {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (pause *ownedMainPauseV2) CheckPaused(ctx context.Context) error {
	if pause == nil || pause.main == nil || pause.holder == nil || ctx == nil {
		return sessionLeaseError("the original bounded main pause is absent")
	}
	pause.main.mu.Lock()
	defer pause.main.mu.Unlock()
	if pause.closed || pause.holder.main != pause.main || pause.main.purgePause != pause || pause.main.cmd != pause.holder.cmd {
		return sessionLeaseError("the original bounded pause owner ended or changed")
	}
	state, err := observeOriginalMainV2(ctx, pause.holder, pause.main)
	if err != nil {
		return err
	}
	if pause.wasExited && state == proc.ChildExitedV2 || !pause.wasExited && state == proc.ChildStoppedV2 {
		return nil
	}
	return sessionLeaseError("the original main has no current native quiescent observation")
}

func (pause *ownedMainPauseV2) Restore(ctx context.Context) error {
	if pause == nil || pause.main == nil || pause.holder == nil || ctx == nil {
		return sessionLeaseError("the original main restoration owner is absent")
	}
	pause.main.mu.Lock()
	defer pause.main.mu.Unlock()
	return pause.restoreLockedV2(ctx)
}

func (pause *ownedMainPauseV2) restoreLockedV2(ctx context.Context) error {
	if pause.committed {
		return sessionLeaseError("the original main entered destructive closure and cannot be restored")
	}
	if pause.closed {
		return nil
	}
	if pause.holder.main != pause.main || pause.main.purgePause != pause || pause.main.cmd != pause.holder.cmd {
		return sessionLeaseError("the original main restoration lost its creation owner")
	}
	if pause.changed {
		// An actual/ambiguous destructive Stop is fenced by the higher-level
		// preparation before entering Restore. This method cannot resurrect
		// a process which that or any other actor has already ended.
		if originalMainJoinedV2(pause.holder, pause.main) {
			return sessionLeaseError("the original paused main ended and cannot be restored")
		}
		if err := unix.PidfdSendSignal(pause.main.pidfd, unix.SIGCONT, nil, 0); err != nil {
			return err
		}
		for {
			state, err := observeOriginalMainV2(ctx, pause.holder, pause.main)
			if err != nil {
				return err
			}
			if state == proc.ChildContinuedV2 {
				break
			}
			if state == proc.ChildExitedV2 {
				return sessionLeaseError("the original live main ended before restoration was confirmed")
			}
			if err := waitMainObservationTurnV2(ctx); err != nil {
				return err
			}
		}
	} else if pause.wasStopped || pause.wasExited {
		state, err := observeOriginalMainV2(ctx, pause.holder, pause.main)
		if err != nil {
			return err
		}
		if pause.wasStopped && state != proc.ChildStoppedV2 || pause.wasExited && state != proc.ChildExitedV2 {
			return sessionLeaseError("the original pre-existing main state changed during preparation")
		}
	}
	pause.closed = true
	pause.main.purgePause = nil
	return nil
}
