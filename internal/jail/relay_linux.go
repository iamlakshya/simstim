package jail

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

type sigRelay struct {
	mu       sync.Mutex
	pid      int
	sigs     <-chan os.Signal
	done     chan struct{}
	finished chan struct{}
	once     sync.Once
	kill     func(int, syscall.Signal) error
	wait4    func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error)
	check    func() // a test observes the reaping mutex before disarm
}

// childReaped lets a test observe an orphan reaped while the main command
// remains alive. It runs under the relay's ownership mutex.
var childReaped func(main, child int)

// catchForwarded starts before the stage creates its child. Keep this
// registration until stage exit: disarming must not restore SIGTERM's
// default action while owned-child cleanup or status output is unfinished.
func catchForwarded() <-chan os.Signal {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, forwarded...)
	return sigs
}

func startRelay(pid int, sigs <-chan os.Signal, kill func(int, syscall.Signal) error) *sigRelay {
	r := &sigRelay{pid: pid, sigs: sigs, done: make(chan struct{}), finished: make(chan struct{}),
		kill: kill, wait4: syscall.Wait4}
	go func() {
		defer close(r.finished)
		for {
			select {
			case <-r.done:
				return
			case s := <-r.sigs:
				if sig, ok := s.(syscall.Signal); ok {
					r.forward(sig)
				}
			}
		}
	}()
	return r
}

func (r *sigRelay) forward(sig syscall.Signal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pid > 0 {
		_ = r.kill(r.pid, sig)
	}
}

func (r *sigRelay) disarmLocked() {
	if r.check != nil {
		r.check()
	}
	r.pid = 0
	r.once.Do(func() { close(r.done) })
}

func (r *sigRelay) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disarmLocked()
}

// reapWithRelay waits in the kernel without reaping, then serialises each
// nonblocking reap with signal forwarding. The main PID becomes unusable
// before releasing the mutex that protects its last ownership claim.
func reapWithRelay(pid int, r *sigRelay) (syscall.WaitStatus, error) {
	for {
		if err := waitExited(unix.P_ALL, 0, unix.WALL); err != nil {
			r.stop()
			return 0, err
		}
		for {
			var ws syscall.WaitStatus
			r.mu.Lock()
			got, err := r.wait4(-1, &ws, unix.WNOHANG|unix.WALL, nil)
			ended := got == pid || errors.Is(err, syscall.ECHILD)
			if ended {
				r.disarmLocked()
			}
			if got > 0 && childReaped != nil {
				childReaped(pid, got)
			}
			r.mu.Unlock()
			if ended {
				return ws, nil
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				r.stop()
				return 0, stepFailed("cannot reap the jail's command", "Check that the host permits waiting for child processes.", err)
			}
			if got == 0 {
				break
			}
		}
	}
}

func waitExited(kind, pid, extra int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(kind, pid, &info, unix.WEXITED|unix.WNOWAIT|extra, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return stepFailed("cannot wait for the jail's command", "Check that the host permits waiting for child processes.", err)
		}
		return nil
	}
}
