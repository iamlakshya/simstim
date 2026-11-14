package client

import (
	"errors"
	"io"
	"os"
)

func (l *agentNativeLog) pump() {
	defer close(l.done)
	buffer := make([]byte, agentNativeLogBuffer)
	for {
		progress, ended, err := l.pumpStep(buffer)
		if err != nil {
			l.fail(err)
			return
		}
		if ended {
			return
		}
		if !progress {
			if err := l.pollReader(); err != nil {
				stopped := l.closed.Load() || l.Failure() != nil
				if !stopped {
					l.fail(agentNativeLogError("the FIFO poll failed"))
				}
				return
			}
		}
	}
}

func (l *agentNativeLog) pumpStep(buffer []byte) (progress bool, ended bool, failure error) {
	if err := l.lockContext(l.ctx); err != nil {
		return false, true, nil
	}
	defer func() {
		// A failed read must become sticky before Sync can sample the
		// reduced kernel queue. Owner cancellation happens after unlocking.
		if failure != nil {
			l.control.Lock()
			if l.failure == nil && !l.closed.Load() {
				l.failure = failure
			}
			l.control.Unlock()
			l.notifyLocked()
		}
		l.mu.Unlock()
	}()
	if l.closed.Load() || l.Failure() != nil {
		return false, true, nil
	}
	// Read, totalRead and complete-line commitment are one atomic observer
	// step relative to Sync. The descriptor is nonblocking; polling is outside mu.
	n, err := l.rawReadLocked(buffer)
	if n > 0 {
		if l.afterRead != nil {
			l.afterRead()
		}
		l.read += int64(n)
		if l.closed.Load() || l.Failure() != nil {
			return false, true, nil
		}
		if l.read > agentNativeLogTotal {
			return false, true, agentNativeLogError("the native log exceeds 1 MiB")
		}
		l.releaseStartupWriter()
		if err := l.provenanceLocked(); err != nil {
			return false, true, err
		}
		if consumeErr := l.consumeLocked(buffer[:n]); consumeErr != nil {
			return false, true, consumeErr
		}
		l.notifyLocked()
	}
	if errors.Is(err, io.EOF) {
		l.eof = true
		l.notifyLocked()
		if len(l.pending) != 0 {
			return n > 0, true, agentNativeLogError("the native log ended in an incomplete line")
		}
		return n > 0, true, nil
	}
	if err != nil {
		return n > 0, true, agentNativeLogError("the native FIFO read failed")
	}
	return n > 0, false, nil
}

func (l *agentNativeLog) consumeLocked(input []byte) error {
	for _, value := range input {
		if value == 0 {
			return agentNativeLogError("the native log contains a NUL byte")
		}
		if len(l.pending) == agentNativeLogLine {
			return agentNativeLogError("a native log line exceeds 64 KiB")
		}
		l.pending = append(l.pending, value)
		if value != '\n' {
			continue
		}
		if l.closed.Load() {
			return ErrClosed
		}
		if failure := l.Failure(); failure != nil {
			return failure
		}
		n, err := l.regular.Write(l.pending)
		if err != nil || n != len(l.pending) {
			return agentNativeLogError("the regular transcript write failed")
		}
		l.committed += int64(n)
		l.pending = l.pending[:0]
	}
	return nil
}

func (l *agentNativeLog) releaseStartupWriter() {
	l.dummyOnce.Do(func() {
		go func() {
			defer close(l.dummyDone)
			if l.dummy != nil {
				_ = l.dummy.Close()
			}
		}()
	})
}

func (l *agentNativeLog) interruptInput() {
	l.cancel()
	l.releaseStartupWriter()
	l.readerOnce.Do(func() {
		go func() {
			defer close(l.inputDone)
			if l.fifo != nil {
				_ = l.fifo.Close()
			}
		}()
	})
}

func (l *agentNativeLog) provenanceLocked() error {
	if l.root == nil || l.parent == nil || l.regular == nil || l.fifo == nil {
		return agentNativeLogError("the capture is not fully initialised")
	}
	currentDir, err := l.parent.Lstat(l.dirName)
	openedDir, openedErr := l.root.Stat(".")
	if err != nil || openedErr != nil || !agentNativeOwnedInfo(currentDir, os.ModeDir) || currentDir.Mode().Perm() != 0o700 ||
		!os.SameFile(currentDir, l.dirInfo) || !os.SameFile(openedDir, l.dirInfo) {
		return agentNativeLogError("the private capture folder changed")
	}
	for _, file := range []struct {
		name, witness string
		info          os.FileInfo
		kind          os.FileMode
	}{
		{"transcript.log", ".regular-owned", l.regularInfo, 0},
		{"native.fifo", ".fifo-owned", l.fifoInfo, os.ModeNamedPipe},
	} {
		current, err := l.root.Lstat(file.name)
		retained, retainedErr := l.root.Lstat(file.witness)
		if err != nil || retainedErr != nil || !agentNativeOwnedInfo(current, file.kind) || current.Mode().Perm() != 0o600 ||
			!os.SameFile(current, file.info) || !os.SameFile(current, retained) {
			return agentNativeLogError("a private capture path changed")
		}
	}
	regular, err := l.regular.Stat()
	if err != nil || !os.SameFile(regular, l.regularInfo) || regular.Size() != l.committed {
		return agentNativeLogError("the regular transcript inode or size changed")
	}
	return nil
}

func (l *agentNativeLog) openSnapshotLocked() (*os.File, int64, error) {
	if err := l.usableLocked(); err != nil {
		return nil, 0, err
	}
	if err := l.provenanceLocked(); err != nil {
		return nil, 0, err
	}
	file, err := l.root.Open("transcript.log")
	if err != nil {
		return nil, 0, agentNativeLogError("the regular transcript snapshot could not be opened")
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, l.regularInfo) || !agentNativeOwnedInfo(info, 0) || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, 0, agentNativeLogError("the regular transcript snapshot changed")
	}
	return file, l.committed, nil
}
