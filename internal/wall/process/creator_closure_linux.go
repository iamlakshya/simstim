//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// processCreatorClosureV3 comes only from the actual factory's pre-spawn pipe
// and the atomic pidfd returned by that SAME exec.Cmd.Start. No setter exists.
type processCreatorClosureV3 struct {
	mu                    sync.Mutex
	completion            *os.File
	completionFD          int
	pidfd                 int
	received, eof, proven bool
	failure               error
}

func (creator *processCreatorClosureV3) check() (bool, error) {
	if creator == nil {
		return false, fmt.Errorf("original process creator closure is absent")
	}
	creator.mu.Lock()
	defer creator.mu.Unlock()
	if creator.failure != nil {
		return false, creator.failure
	}
	if creator.proven {
		return true, nil
	}
	if creator.completion == nil || creator.pidfd < 0 {
		return false, fmt.Errorf("original process creator lost its native handles")
	}
	poll := []unix.PollFd{{Fd: int32(creator.pidfd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, 0)
	if err != nil {
		return false, err
	}
	ready, err := pidfdExitReady(n, poll[0].Revents)
	if err != nil || !ready {
		return false, err
	}
	if err := unix.PidfdSendSignal(creator.pidfd, 0, nil, 0); !errors.Is(err, unix.ESRCH) {
		return false, fmt.Errorf("original creator has no exited native pidfd: %w", err)
	}
	for !creator.eof {
		var receipt [2]byte
		n, err := unix.Read(creator.completionFD, receipt[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			creator.eof = true
			break
		}
		if creator.received || n != 1 || receipt[0] != 'C' {
			creator.failure = fmt.Errorf("original creator supplied an invalid or repeated native completion")
			return false, creator.failure
		}
		creator.received = true
	}
	if !creator.received {
		creator.failure = fmt.Errorf("original creator exited without native child completion")
		return false, creator.failure
	}
	creator.proven = true
	return true, nil
}

func (creator *processCreatorClosureV3) close() error {
	if creator == nil {
		return nil
	}
	creator.mu.Lock()
	defer creator.mu.Unlock()
	if !creator.proven {
		return fmt.Errorf("original creator handles remain retained until native completion")
	}
	var failure error
	if creator.completion != nil {
		failure = creator.completion.Close()
		creator.completion = nil
	}
	if creator.pidfd >= 0 {
		failure = errors.Join(failure, unix.Close(creator.pidfd))
		creator.pidfd = -1
	}
	return failure
}

func (creator *processCreatorClosureV3) signal(signal unix.Signal) error {
	if creator == nil {
		return fmt.Errorf("original creator native signal owner is absent")
	}
	creator.mu.Lock()
	defer creator.mu.Unlock()
	if creator.proven {
		return nil
	}
	if creator.pidfd < 0 {
		return fmt.Errorf("original creator has no atomic process handle for signalling")
	}
	err := unix.PidfdSendSignal(creator.pidfd, signal, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
