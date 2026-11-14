package proc

import (
	"io"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// dupPeer makes a close-on-exec copy of fd and reports whether the file is
// non-blocking and whether it is open for writing only.
func dupPeer(fd int) (dup int, nonblock, writeOnly bool) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return -1, false, false
	}
	n, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, false, false
	}
	return n, flags&unix.O_NONBLOCK != 0, flags&unix.O_ACCMODE == unix.O_WRONLY
}

// hungUp reports, without waiting, whether the peer of fd has closed its end:
// poll sets POLLRDHUP, POLLHUP or POLLERR even while unread bytes wait.
func hungUp(fd int, writeOnly bool) bool {
	events := int16(unix.POLLRDHUP)
	if writeOnly {
		events = 0
	}
	p := []unix.PollFd{{Fd: int32(fd), Events: events}} //nolint:gosec // G115: a file descriptor is small
	n, err := unix.Poll(p, 0)
	return err == nil && n > 0 && p[0].Revents&(unix.POLLRDHUP|unix.POLLHUP|unix.POLLERR) != 0
}

type pollWatch struct {
	mu      sync.Mutex
	wakeW   int
	stopped atomic.Bool
	once    sync.Once
	done    chan struct{}
}

func (w *pollWatch) Close() error {
	w.once.Do(func() {
		w.stopped.Store(true)
		w.closeWake()
		<-w.done
	})
	return nil
}

func (w *pollWatch) closeWake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wakeW >= 0 {
		_ = unix.Close(w.wakeW)
		w.wakeW = -1
	}
}

// watchBlocking watches a blocking descriptor (such as an inherited pipe)
// through poll without changing O_NONBLOCK on the shared file description.
func watchBlocking(fd int, writeOnly bool, gone func()) io.Closer {
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		_ = unix.Close(fd)
		return nil
	}
	events := int16(unix.POLLRDHUP)
	if writeOnly {
		events = 0
	}
	w := &pollWatch{wakeW: p[1], done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer w.closeWake()
		defer unix.Close(p[0])
		defer unix.Close(fd)
		fds := []unix.PollFd{
			{Fd: int32(fd), Events: events},        //nolint:gosec // G115: a file descriptor is small
			{Fd: int32(p[0]), Events: unix.POLLIN}, //nolint:gosec // G115: a file descriptor is small
		}
		for {
			_, err := unix.Poll(fds, -1)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				return
			}
			if fds[1].Revents != 0 {
				return
			}
			if fds[0].Revents&(unix.POLLRDHUP|unix.POLLHUP|unix.POLLERR) != 0 {
				if !w.stopped.Load() {
					gone()
				}
				return
			}
		}
	}()
	return w
}
