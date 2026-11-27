package client

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type agentIndexLease struct {
	mode    *agentModeResources
	ctx     context.Context
	cancel  context.CancelFunc
	stop    func() bool
	done    chan struct{}
	mu      sync.Mutex
	file    *os.File
	info    os.FileInfo
	closed  bool
	pending bool
	once    sync.Once
	err     error
}

type agentIndexFile struct{ lease *agentIndexLease }

func (l *agentIndexLease) File() tomlinspect.IndexFile { return agentIndexFile{lease: l} }

func (l *agentIndexLease) Workspace() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.checkLocked() != nil {
		return nil
	}
	return l.mode.workspace
}

func (l *agentIndexLease) checkLocked() error {
	if l.closed || l.mode.ctx.Err() != nil {
		return ErrClosed
	}
	return l.ctx.Err()
}

// Close is called by the inspector only after its last workspace access.
// It never clears a slab underneath an active parser or blocked native call.
func (l *agentIndexLease) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		l.err = l.checkLocked()
		l.closed = true
		l.cancel()
		l.stop()
		if l.file != nil {
			l.err = errors.Join(l.err, l.removePending())
			l.err = errors.Join(l.err, l.file.Close())
			l.file = nil
		}
		l.mu.Unlock()
		m := l.mode
		m.mu.Lock()
		clear(m.workspace)
		m.active = nil
		if m.closed && m.nativeLog == nil {
			m.cleanupLocked()
			l.err = errors.Join(l.err, m.closeErr)
		}
		m.mu.Unlock()
		m.turn <- struct{}{}
		close(l.done)
	})
	return l.err
}

func (f agentIndexFile) ReadAt(p []byte, offset int64) (int, error) {
	l := f.lease
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkLocked(); err != nil {
		return 0, err
	}
	n, err := l.file.ReadAt(p, offset)
	if stopped := l.checkLocked(); stopped != nil {
		return n, stopped
	}
	return n, err
}

func (f agentIndexFile) WriteAt(p []byte, offset int64) (int, error) {
	l := f.lease
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkLocked(); err != nil {
		return 0, err
	}
	n, err := l.file.WriteAt(p, offset)
	if stopped := l.checkLocked(); stopped != nil {
		return n, stopped
	}
	return n, err
}

func (f agentIndexFile) Stat() (fs.FileInfo, error) {
	l := f.lease
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkLocked(); err != nil {
		return nil, err
	}
	if err := l.mode.checkAgentSettingsMode(l.ctx); err != nil {
		return nil, err
	}
	info, err := l.file.Stat()
	if err != nil {
		return nil, err
	}
	if !agentIndexOwnedInfo(info, 0) || !os.SameFile(info, l.info) {
		return nil, agentModeResourceError()
	}
	if err := l.checkLocked(); err != nil {
		return nil, err
	}
	return info, nil
}
