package host

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// holderMainV1 belongs to the actual PTY Start, before any Wait goroutine.
// The optional Linux descriptor is supplied atomically by that creation.
type holderMainV1 struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	pidfd      int
	joined     bool
	purgePause *ownedMainPauseV2
}

// joinOwnedMainV1 never signals a saved PID or process group. Closing the
// actual PTY lets the kernel hang up that terminal's foreground processes.
// N5 still permits unconfined children to leave that terminal and parent.
func (h *Holder) joinOwnedMainV1() error {
	if h.main == nil || h.main.cmd != h.cmd || h.cmd == nil || h.cmd.Process == nil || h.ptmx == nil || h.waitDone == nil || h.readDone == nil {
		return sessionLeaseError("the holder has no original main creation and terminal to join")
	}
	main := h.main
	main.mu.Lock()
	defer main.mu.Unlock()
	if main.joined {
		return nil
	}
	if main.purgePause != nil {
		main.purgePause.committed = true
		main.purgePause = nil
		// Commit a genuine held pause without running a HUP handler or
		// terminal-generated continue between final inspection and stop.
		if err := signalHolderMainV1(main, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	signalMain := signalHolderMainV1
	if h.guest != nil {
		signalMain = signalHolderMainGroupV1
	}
	if err := signalMain(main, syscall.SIGHUP); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	// This is the original owned terminal, not a later foreground group ID.
	if err := h.ptmx.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	select {
	case <-h.waitDone:
	case <-time.After(3 * time.Second):
		if err := signalMain(main, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		<-h.waitDone
	}
	<-h.readDone
	if h.vt != nil {
		if err := h.vt.Close(); err != nil {
			return err
		}
	}
	if h.ptyOut != nil {
		close(h.ptyOut)
		<-h.ptyOutDone
		h.ptyOut = nil
	}
	main.joined = true
	return nil
}
