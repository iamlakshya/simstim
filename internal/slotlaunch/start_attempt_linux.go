//go:build linux

package slotlaunch

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// nativeStartAttemptV1 belongs to the actual Cmd constructor before Start.
// It retains the original atomic creator handle and any verified root worker.
// Its exit is NOT a whole-allocation proof: keeper/init may have outlived it.
type nativeStartAttemptV1 struct {
	mu              sync.Mutex
	command         *exec.Cmd
	creatorFD       int
	creator, worker *os.File
	partial         []*os.File
	waitOnce        sync.Once
	waitErr         error
	closeOnce       sync.Once
	closeErr        error
}

func newNativeStartAttemptV1(command *exec.Cmd) *nativeStartAttemptV1 {
	owner := &nativeStartAttemptV1{command: command, creatorFD: -1}
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &owner.creatorFD}
	return owner
}

func (attempt *nativeStartAttemptV1) start() error {
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	err := attempt.command.Start()
	if attempt.creatorFD >= 0 {
		attempt.creator = os.NewFile(uintptr(attempt.creatorFD), "original-root-start-creator")
	}
	if err != nil {
		return err
	}
	if attempt.creator == nil {
		return refused("the actual root start creator has no atomic kernel creation handle; retain its original operation")
	}
	return nil
}

func (attempt *nativeStartAttemptV1) captureRootWorker(file *os.File) error {
	if attempt == nil || file == nil {
		return refused("the native start has no actual root worker")
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.worker != nil {
		left, err := nativePID(attempt.worker)
		if err != nil {
			return err
		}
		right, err := nativePID(file)
		if err != nil || left != right || !pidLive(attempt.worker) || !pidLive(file) {
			return refused("the native handoff changed its actual original root worker")
		}
		return nil
	}
	copy, err := dupFile(file)
	if err != nil {
		return err
	}
	attempt.worker = copy
	return nil
}

func (attempt *nativeStartAttemptV1) stopActors() error {
	if attempt == nil {
		return refused("the actual native start attempt is absent")
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	var result error
	seen := false
	for _, actor := range []*os.File{attempt.worker, attempt.creator} {
		if actor == nil {
			continue
		}
		seen = true
		if err := unix.PidfdSendSignal(int(actor.Fd()), unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			result = errors.Join(result, err)
		}
	}
	if !seen {
		return refused("the incomplete root start has no captured actor to signal; retain its original whole unit and operation")
	}
	return result
}

func (attempt *nativeStartAttemptV1) waitCreator() error {
	if attempt == nil {
		return refused("the actual native start attempt is absent")
	}
	attempt.waitOnce.Do(func() {
		attempt.waitErr = attempt.command.Wait()
		attempt.mu.Lock()
		creator, worker := attempt.creator, attempt.worker
		attempt.mu.Unlock()
		if creator == nil {
			attempt.waitErr = errors.Join(attempt.waitErr, refused("the root start's actual creator remains unproved"))
			return
		}
		for !pidExited(creator) {
			time.Sleep(10 * time.Millisecond)
		}
		if worker != nil {
			for !pidExited(worker) {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	return attempt.waitErr
}

// Only the fully adopted NativeSource calls this AFTER its positive whole
// cgroup/keeper closure. A partial handoff cannot use creator exit as a permit.
func (attempt *nativeStartAttemptV1) closeJoined() error {
	if attempt == nil {
		return nil
	}
	attempt.closeOnce.Do(func() {
		attempt.closeErr = attempt.waitCreator()
		attempt.mu.Lock()
		defer attempt.mu.Unlock()
		if attempt.worker != nil {
			attempt.closeErr = errors.Join(attempt.closeErr, attempt.worker.Close())
		}
		if attempt.creator != nil {
			attempt.closeErr = errors.Join(attempt.closeErr, attempt.creator.Close())
		}
		for _, file := range attempt.partial {
			attempt.closeErr = errors.Join(attempt.closeErr, file.Close())
		}
	})
	return attempt.closeErr
}

func (attempt *nativeStartAttemptV1) retainPartial(files []*os.File) {
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	attempt.partial = append(attempt.partial, files...)
}
