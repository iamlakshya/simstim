package proc

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"syscall"

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
// kqueue sets EV_EOF even while unread bytes wait in the pipe or socket.
func hungUp(fd int, writeOnly bool) bool {
	kq, err := newPeerKqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	filter := int16(unix.EVFILT_READ)
	if writeOnly {
		filter = unix.EVFILT_WRITE
	}
	change := []unix.Kevent_t{{Ident: uint64(fd), Filter: filter, Flags: unix.EV_ADD}} //nolint:gosec // G115: a file descriptor is small
	ev := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(kq, change, ev, &unix.Timespec{})
	return err == nil && n > 0 && ev[0].Flags&unix.EV_EOF != 0
}

func newPeerKqueue() (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	kq, err := unix.Kqueue()
	if err != nil {
		return -1, fmt.Errorf("open peer watch: %w", err)
	}
	unix.CloseOnExec(kq)
	return kq, nil
}

type kqWatch struct {
	mu      sync.Mutex
	kq      int
	stopped atomic.Bool
	once    sync.Once
	done    chan struct{}
}

func (w *kqWatch) Close() error {
	w.once.Do(func() {
		w.stopped.Store(true)
		w.mu.Lock()
		if w.kq >= 0 {
			wake := []unix.Kevent_t{{Ident: 1, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}}
			_, _ = unix.Kevent(w.kq, wake, nil, &unix.Timespec{})
		}
		w.mu.Unlock()
		<-w.done
	})
	return nil
}

func (w *kqWatch) closeKqueue() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.kq >= 0 {
		_ = unix.Close(w.kq)
		w.kq = -1
	}
}

// watchBlocking watches a blocking descriptor (such as an inherited pipe)
// through kqueue without changing O_NONBLOCK on the shared file description.
func watchBlocking(fd int, writeOnly bool, gone func()) io.Closer {
	kq, err := newPeerKqueue()
	if err != nil {
		_ = unix.Close(fd)
		return nil
	}
	filter := int16(unix.EVFILT_READ)
	if writeOnly {
		filter = unix.EVFILT_WRITE
	}
	reg := []unix.Kevent_t{
		{Ident: uint64(fd), Filter: filter, Flags: unix.EV_ADD | unix.EV_CLEAR}, //nolint:gosec // G115: a file descriptor is small
		{Ident: 1, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_CLEAR},
	}
	if _, err := unix.Kevent(kq, reg, nil, &unix.Timespec{}); err != nil {
		_ = unix.Close(kq)
		_ = unix.Close(fd)
		return nil
	}
	w := &kqWatch{kq: kq, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer w.closeKqueue()
		defer unix.Close(fd)
		ev := make([]unix.Kevent_t, 2)
		for {
			n, err := unix.Kevent(kq, nil, ev, nil)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				return
			}
			for _, e := range ev[:n] {
				if e.Filter == unix.EVFILT_USER {
					return
				}
				if e.Flags&unix.EV_EOF != 0 {
					if !w.stopped.Load() {
						gone()
					}
					return
				}
			}
		}
	}()
	return w
}
