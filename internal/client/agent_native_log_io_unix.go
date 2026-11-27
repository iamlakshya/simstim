//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func makeAgentNativeLog(parentContext context.Context) (*agentNativeLog, error) {
	directory, err := newAgentModeDirectory()
	if err != nil {
		return nil, err
	}
	return makeAgentNativeLogAtDirectory(parentContext, directory, true)
}

func makeAgentNativeLogAtDirectory(parentContext context.Context, directory *agentModeDirectory, owns bool) (result *agentNativeLog, err error) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parentContext))
	l := &agentNativeLog{ctx: ctx, cancel: cancel, startup: parentContext, changed: make(chan struct{}), stop: make(chan struct{}),
		done: make(chan struct{}), inputDone: make(chan struct{}), dummyDone: make(chan struct{}), cleanupDone: make(chan struct{}),
		pending: make([]byte, 0, agentNativeLogLine), scanTurn: make(chan struct{}, 1)}
	l.scanTurn <- struct{}{}
	started, keep := false, false
	defer func() {
		if !keep {
			if !started {
				close(l.done)
			}
			if owns {
				_ = l.Close()
			} else {
				// The shared mode retains this partial borrower before cleanup
				// starts, including when native cleanup outlives its caller.
				result = l
			}
		}
	}()
	if err := l.borrowDirectory(directory, owns); err != nil {
		return nil, err
	}
	if err := l.createTranscript(); err != nil {
		return nil, err
	}
	if err := l.createFIFO(); err != nil {
		return nil, err
	}
	if err := l.provenanceLocked(); err != nil {
		return nil, err
	}
	l.parentStop = context.AfterFunc(parentContext, func() { l.fail(parentContext.Err()) })
	started = true
	go l.pump()
	if err := parentContext.Err(); err != nil {
		return nil, err
	}
	keep = true
	return l, nil
}

func (l *agentNativeLog) createTranscript() error {
	file, err := l.root.OpenFile("transcript.log", os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return agentNativeLogError("the regular transcript could not be created")
	}
	l.regular = file
	l.regularInfo, err = file.Stat()
	if err != nil || !agentNativeOwnedInfo(l.regularInfo, 0) || l.regularInfo.Mode().Perm() != 0o600 {
		return agentNativeLogError("the regular transcript is unsafe")
	}
	if err := l.root.Link("transcript.log", ".regular-owned"); err != nil {
		return agentNativeLogError("the regular transcript ownership could not be retained")
	}
	return nil
}

func (l *agentNativeLog) createFIFO() error {
	if err := unix.Mkfifo(l.path, 0o600); err != nil {
		return agentNativeLogError("the private log FIFO could not be created")
	}
	info, err := l.root.Lstat("native.fifo")
	if err != nil || !agentNativeOwnedInfo(info, os.ModeNamedPipe) || info.Mode().Perm() != 0o600 {
		return agentNativeLogError("the private log FIFO is unsafe")
	}
	l.fifoInfo = info
	if err := l.root.Link("native.fifo", ".fifo-owned"); err != nil {
		return agentNativeLogError("the private FIFO ownership could not be retained")
	}
	l.fifo, err = l.root.OpenFile("native.fifo", os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return agentNativeLogError("the log FIFO reader could not be opened")
	}
	opened, err := l.fifo.Stat()
	if err != nil || !os.SameFile(opened, info) {
		return agentNativeLogError("the log FIFO reader changed while opening")
	}
	l.raw, err = l.fifo.SyscallConn()
	if err != nil {
		return agentNativeLogError("the log FIFO reader has no owned descriptor")
	}
	var flagError error
	if err := l.raw.Control(func(fd uintptr) { flagError = unix.SetNonblock(int(fd), true) }); err != nil || flagError != nil {
		return agentNativeLogError("the log FIFO reader could not be made nonblocking")
	}
	// The reader exists before this writer. The writer prevents startup EOF
	// until the real process has emitted bytes or Finish releases it.
	l.dummy, err = l.root.OpenFile("native.fifo", os.O_WRONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return agentNativeLogError("the log FIFO startup writer could not be opened")
	}
	opened, err = l.dummy.Stat()
	if err != nil || !os.SameFile(opened, info) {
		return agentNativeLogError("the log FIFO startup writer changed while opening")
	}
	return nil
}

func agentNativeOwnedInfo(info os.FileInfo, kind os.FileMode) bool {
	if info == nil || info.Mode().Type() != kind {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid())
}

func (l *agentNativeLog) rawReadLocked(buffer []byte) (int, error) {
	var n int
	var readError error
	err := l.raw.Control(func(fd uintptr) {
		for {
			n, readError = unix.Read(int(fd), buffer)
			if !errors.Is(readError, unix.EINTR) {
				return
			}
		}
	})
	if err != nil {
		return 0, agentNativeLogError("the FIFO descriptor could not be read")
	}
	if errors.Is(readError, unix.EAGAIN) || errors.Is(readError, unix.EWOULDBLOCK) {
		return 0, nil
	}
	if n == 0 && readError == nil {
		return 0, io.EOF
	}
	if readError != nil {
		return 0, agentNativeLogError("the FIFO read failed")
	}
	return n, nil
}

func (l *agentNativeLog) unreadLocked() (int, error) {
	var unread int
	var ioctlError error
	err := l.raw.Control(func(fd uintptr) { unread, ioctlError = unix.IoctlGetInt(int(fd), agentNativeFIONREAD) })
	if err != nil {
		return 0, agentNativeLogError("the FIFO descriptor could not be sampled")
	}
	if ioctlError != nil {
		return 0, agentNativeLogError("the FIFO queue could not be sampled")
	}
	return unread, nil
}

func (l *agentNativeLog) pollReader() error {
	var pollError error
	err := l.raw.Control(func(fd uintptr) {
		_, pollError = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}, 25)
	})
	if err != nil {
		return agentNativeLogError("the FIFO descriptor could not be polled")
	}
	if errors.Is(pollError, unix.EINTR) {
		return nil
	}
	if pollError != nil {
		return agentNativeLogError("the FIFO poll failed")
	}
	return nil
}
