package client

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	agentNativeLogTotal        = 1 << 20
	agentNativeLogLine         = 64 << 10
	agentNativeLogDisplayLimit = 8 << 10
	agentNativeLogBuffer       = 32 << 10
)

type agentNativeLog struct {
	mu          sync.Mutex
	control     sync.Mutex
	changed     chan struct{}
	stop        chan struct{}
	done        chan struct{}
	inputDone   chan struct{}
	dummyDone   chan struct{}
	cleanupDone chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	startup     context.Context

	parentStop func() bool
	owner      func(error)
	attached   bool
	notified   bool
	failure    error
	closed     atomic.Bool
	eof        bool
	read       int64
	committed  int64
	pending    []byte
	scanTurn   chan struct{}
	scan       *os.File
	afterRead  func()

	directory                        *agentModeDirectory
	mode                             *agentModeResources
	ownsDirectory                    bool
	parent, root                     *os.Root
	dirName, path                    string
	dirInfo, fifoInfo, regularInfo   os.FileInfo
	fifo, dummy, regular             *os.File
	raw                              syscall.RawConn
	dummyOnce, readerOnce, closeOnce sync.Once
	finishOnce                       sync.Once
	finishResult                     agentNativeLogSummary
	finishErr, closeErr              error
}

func newAgentNativeLog(ctx context.Context) (*agentNativeLog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return makeAgentNativeLog(ctx)
}

// Path is only the private FIFO passed to the owned SSH command's -E option.
func (l *agentNativeLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// AttachOwner assigns one owned process cancellation callback. It must not
// wait for that process, the pump or this log to close.
func (l *agentNativeLog) AttachOwner(owner func(error)) error {
	if owner == nil {
		return agentNativeLogError("no owned process was assigned")
	}
	l.control.Lock()
	if l.attached || l.closed.Load() {
		l.control.Unlock()
		err := agentNativeLogError("the private log was reused or closed")
		l.fail(err)
		owner(err)
		return err
	}
	l.attached, l.owner = true, owner
	if l.startup != nil && l.startup.Err() != nil && l.failure == nil {
		l.failure = l.startup.Err()
	}
	if l.parentStop != nil {
		l.parentStop()
	}
	if l.startup != nil && l.startup.Err() != nil && l.failure == nil {
		l.failure = l.startup.Err()
	}
	l.startup, l.parentStop = nil, nil
	callback, failure := l.failureCallbackLocked()
	l.control.Unlock()
	if callback != nil {
		callback(failure)
	}
	if failure != nil {
		l.interruptInput()
	}
	return failure
}

// Failure is the first capture failure. It never reads the transcript.
func (l *agentNativeLog) Failure() error {
	if l == nil {
		return nil
	}
	l.control.Lock()
	defer l.control.Unlock()
	return l.failure
}

// Sync commits every byte queued before the caller's native ACK barrier.
// The native logger must be blocked awaiting that ACK while this is called.
func (l *agentNativeLog) Sync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.lockContext(ctx); err != nil {
		return err
	}
	if err := l.usableLocked(); err != nil {
		l.mu.Unlock()
		return err
	}
	if err := l.provenanceLocked(); err != nil {
		l.mu.Unlock()
		l.fail(err)
		return err
	}
	unread, err := l.unreadLocked()
	if err != nil {
		l.mu.Unlock()
		err = agentNativeLogError("the native log queue could not be sampled")
		l.fail(err)
		return err
	}
	if unread < 0 || int64(unread) > agentNativeLogTotal-l.read {
		l.mu.Unlock()
		err = agentNativeLogError("the queued log exceeds its capture bound")
		l.fail(err)
		return err
	}
	target := l.read + int64(unread)
	for {
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		if err := l.usableLocked(); err != nil {
			l.mu.Unlock()
			return err
		}
		if l.read >= target {
			if len(l.pending) != 0 || l.committed < target {
				l.mu.Unlock()
				err := agentNativeLogError("the native log prefix ends in an incomplete line")
				l.fail(err)
				return err
			}
			l.mu.Unlock()
			return nil
		}
		if l.eof {
			l.mu.Unlock()
			err := agentNativeLogError("the native log ended before its sampled prefix")
			l.fail(err)
			return err
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.stop:
			return ErrClosed
		case <-l.ctx.Done():
			if failure := l.Failure(); failure != nil {
				return failure
			}
			return ErrClosed
		case <-changed:
		}
		if err := l.lockContext(ctx); err != nil {
			return err
		}
	}
}

func (l *agentNativeLog) lockContext(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-l.stop:
			timer.Stop()
			return ErrClosed
		case <-l.ctx.Done():
			timer.Stop()
			if failure := l.Failure(); failure != nil {
				return failure
			}
			return ErrClosed
		case <-timer.C:
		}
	}
}

// ScanSnapshot gives the verifier a complete bounded prefix on the same
// regular inode. It never allocates the transcript as a byte slice.
func (l *agentNativeLog) ScanSnapshot(ctx context.Context, scan func(io.Reader) error) error {
	if scan == nil {
		return agentNativeLogError("no native log verifier was supplied")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.stop:
		return ErrClosed
	case <-l.ctx.Done():
		if failure := l.Failure(); failure != nil {
			return failure
		}
		return ErrClosed
	case <-l.scanTurn:
	}
	defer func() { l.scanTurn <- struct{}{} }()
	if err := l.Sync(ctx); err != nil {
		return err
	}
	if err := l.lockContext(ctx); err != nil {
		return err
	}
	file, size, err := l.openSnapshotLocked()
	if err == nil {
		l.control.Lock()
		if l.closed.Load() {
			err = ErrClosed
		} else {
			l.scan = file
		}
		l.control.Unlock()
	}
	l.mu.Unlock()
	if err != nil {
		if file != nil {
			go func() { _ = file.Close() }()
		}
		l.fail(err)
		return err
	}
	defer file.Close()
	err = scan(&agentNativeSnapshotReader{ctx: ctx, stop: l.stop, log: l, reader: io.NewSectionReader(file, 0, size)})
	l.control.Lock()
	if l.scan == file {
		l.scan = nil
	}
	l.control.Unlock()
	if err := l.lockContext(ctx); err != nil {
		return err
	}
	check := l.usableLocked()
	if check == nil {
		check = l.provenanceLocked()
	}
	l.mu.Unlock()
	if check != nil {
		if !errors.Is(check, ErrClosed) {
			l.fail(check)
		}
		return check
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type agentNativeSnapshotReader struct {
	ctx    context.Context
	stop   <-chan struct{}
	log    *agentNativeLog
	reader io.Reader
}

func (r *agentNativeSnapshotReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	select {
	case <-r.stop:
		return 0, ErrClosed
	default:
		n, err := r.reader.Read(p)
		if err == nil || err == io.EOF {
			return n, err
		}
		if r.log.closed.Load() {
			return n, ErrClosed
		}
		failure := agentNativeLogError("the regular transcript snapshot could not be read")
		r.log.fail(failure)
		return n, failure
	}
}

func (l *agentNativeLog) usableLocked() error {
	if failure := l.Failure(); failure != nil {
		return failure
	}
	if l.closed.Load() {
		return ErrClosed
	}
	return nil
}

func (l *agentNativeLog) notifyLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *agentNativeLog) failureCallbackLocked() (func(error), error) {
	if l.failure == nil || l.owner == nil || l.notified {
		return nil, l.failure
	}
	l.notified = true
	return l.owner, l.failure
}

func (l *agentNativeLog) fail(err error) {
	l.recordFailure(err, false)
}

func (l *agentNativeLog) recordFailure(err error, stopping bool) {
	if err == nil {
		return
	}
	l.control.Lock()
	if l.failure == nil && (!l.closed.Load() || stopping) {
		l.failure = err
	}
	callback, failure := l.failureCallbackLocked()
	l.control.Unlock()
	if callback != nil {
		callback(failure)
	}
	l.interruptInput()
}

func agentNativeLogError(reason string) error {
	return &UserError{Msg: "the private SSH log cannot verify this agent connection: " + reason,
		Hint: "Check the SSH client, then attach again.", Code: "ssh_agent_refused", Kind: KindRefused}
}
