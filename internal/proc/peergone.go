package proc

import (
	"io"
	"os"
	"syscall"
)

// WatchPeerGone calls gone once the peer of c has closed its end, and returns
// the watch, which Close ends. c may be a socket or a pipe. It reads nothing
// from c, so bytes that wait in the buffer stay there for the reader. It
// watches a close-on-exec copy of the descriptor, so no child inherits it.
// Where the descriptor is non-blocking, Go's poller watches it without a
// dedicated OS thread. Where the system has no such watch, it returns nil.
func WatchPeerGone(c any, gone func()) io.Closer {
	return watchPeerGone(c, gone, peerAuto)
}

// WatchReadPeerGone calls gone when c's peer stops writing, including when
// unread bytes remain. The caller must drain those bytes before treating the
// read side's end as the end of the whole connection.
func WatchReadPeerGone(c any, gone func()) io.Closer {
	return watchPeerGone(c, gone, peerRead)
}

// WatchWritePeerGone calls gone when c's write peer has gone. On a duplex
// socket, the peer closing only its write side does not end this watch.
func WatchWritePeerGone(c any, gone func()) io.Closer {
	return watchPeerGone(c, gone, peerWrite)
}

type peerDirection uint8

const (
	peerAuto peerDirection = iota
	peerRead
	peerWrite
)

func watchPeerGone(c any, gone func(), direction peerDirection) io.Closer {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return nil
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return nil
	}
	fd, nonblock, writeOnly := -1, false, false
	if raw.Control(func(f uintptr) {
		fd, nonblock, writeOnly = dupPeer(int(f))
	}) != nil || fd < 0 {
		return nil
	}
	if direction != peerAuto {
		writeOnly = direction == peerWrite
	}
	if !nonblock {
		return watchBlocking(fd, writeOnly, gone)
	}
	f := os.NewFile(uintptr(fd), "peer")
	rc, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil
	}
	go func() {
		// Go's poller calls the check at once and on each new readiness edge.
		// Close ends the wait with an error.
		var waitErr error
		if writeOnly {
			waitErr = rc.Write(func(fd uintptr) bool { return hungUp(int(fd), true) })
		} else {
			waitErr = rc.Read(func(fd uintptr) bool { return hungUp(int(fd), false) })
		}
		if waitErr == nil {
			gone()
		}
	}()
	return f
}
