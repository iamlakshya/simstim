package client

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// agentModeResources owns the one directory and reusable inspection slab for
// an actual attached-agent lifetime. Construction creates only the directory.
type agentModeResources struct {
	shutdown  agentShutdownBudget
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	directory *agentModeDirectory
	inspector *agentSettingsInspector
	turn      chan struct{}
	workspace []byte
	active    *agentIndexLease
	nativeLog *agentNativeLog
	closed    bool
	closeErr  error
}

type agentModeDirectory struct {
	parent, root *os.Root
	name, path   string
	info         os.FileInfo
}

func newAgentModeResources(ctx context.Context) (*agentModeResources, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := newAgentModeDirectory()
	if err != nil {
		return nil, err
	}
	live, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m := &agentModeResources{ctx: live, cancel: cancel, directory: directory, turn: make(chan struct{}, 1)}
	m.turn <- struct{}{}
	m.inspector, err = newAgentSettingsInspector(m)
	if err == nil {
		err = m.checkAgentSettingsMode(ctx)
	}
	if err != nil {
		cancel()
		return nil, errors.Join(err, directory.close())
	}
	return m, nil
}

func (m *agentModeResources) checkAgentSettingsMode(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil {
		return agentModeResourceError()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkLocked()
}

func (m *agentModeResources) checkLocked() error {
	if m.closed || m.ctx.Err() != nil {
		return ErrClosed
	}
	return m.directory.check()
}

func (d *agentModeDirectory) check() error {
	if d == nil || d.root == nil || d.parent == nil || d.info == nil {
		return agentModeResourceError()
	}
	named, err := d.parent.Lstat(d.name)
	if err != nil || !agentNativeOwnedInfo(named, os.ModeDir) || named.Mode().Perm() != 0o700 || !os.SameFile(named, d.info) {
		return agentModeResourceError()
	}
	opened, err := d.root.Stat(".")
	if err != nil || !agentNativeOwnedInfo(opened, os.ModeDir) || opened.Mode().Perm() != 0o700 || !os.SameFile(opened, d.info) {
		return agentModeResourceError()
	}
	absolute, err := os.Lstat(d.path)
	if err != nil || !os.SameFile(absolute, d.info) {
		return agentModeResourceError()
	}
	return nil
}

func (d *agentModeDirectory) close() error {
	if d == nil {
		return nil
	}
	var err error
	if d.parent != nil && d.info != nil {
		named, failure := d.parent.Lstat(d.name)
		if failure == nil && agentNativeOwnedInfo(named, os.ModeDir) && os.SameFile(named, d.info) {
			if failure := d.parent.Remove(d.name); failure != nil && !errors.Is(failure, os.ErrNotExist) {
				err = agentModeResourceError()
			}
		} else if !errors.Is(failure, os.ErrNotExist) {
			err = agentModeResourceError()
		}
	}
	if err != nil {
		// Keep the held ownership evidence available for a later cleanup.
		return err
	}
	if d.root != nil {
		err = errors.Join(err, d.root.Close())
		d.root = nil
	}
	if d.parent != nil {
		err = errors.Join(err, d.parent.Close())
		d.parent = nil
	}
	return err
}

// close revokes admission first. A timed-out caller leaves the owner retained;
// the active inspector's eventual lease close performs the final cleanup.
// Callers stop the log and collector before closing their shared directory.
func (m *agentModeResources) close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	bounded, stop := m.shutdown.context(ctx, 0)
	defer stop()
	ctx = bounded
	m.mu.Lock()
	m.closed = true
	m.cancel()
	active := m.active
	log := m.nativeLog
	if active == nil && log == nil {
		m.cleanupLocked()
	}
	m.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-ctx.Done():
			return errors.Join(ctx.Err(), agentModeResourceError())
		}
	}
	if log != nil {
		select {
		case <-log.cleanupDone:
		case <-ctx.Done():
			return errors.Join(ctx.Err(), agentModeResourceError())
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeErr
}

func (m *agentModeResources) releaseNativeLog(log *agentNativeLog) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nativeLog == log {
		m.nativeLog = nil
	}
	if m.closed && m.active == nil && m.nativeLog == nil {
		m.cleanupLocked()
	}
}

func (m *agentModeResources) cleanupLocked() {
	clear(m.workspace)
	if m.directory != nil {
		if err := m.directory.close(); err != nil {
			m.closeErr = errors.Join(m.closeErr, err)
		} else {
			m.directory = nil
			m.workspace = nil
		}
	}
}

func (m *agentModeResources) AcquireIndex(ctx context.Context, sourceSize int64) (tomlinspect.IndexLease, error) {
	if sourceSize < 0 || sourceSize > MaxConfigSize {
		return nil, ErrConfigTooLarge
	}
	return m.acquireIndex(ctx)
}

func (m *agentModeResources) acquireIndex(ctx context.Context) (tomlinspect.IndexLease, error) {
	if err := m.checkAgentSettingsMode(ctx); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.ctx.Done():
		return nil, ErrClosed
	case <-m.turn:
	}
	m.mu.Lock()
	if err := m.checkLocked(); err != nil {
		m.mu.Unlock()
		m.turn <- struct{}{}
		return nil, err
	}
	live, cancel := context.WithCancel(ctx)
	lease := &agentIndexLease{mode: m, ctx: live, cancel: cancel, done: make(chan struct{})}
	lease.stop = context.AfterFunc(m.ctx, cancel)
	if m.workspace == nil {
		m.workspace = make([]byte, tomlinspect.IndexWorkspaceBytes)
	}
	m.active = lease
	m.mu.Unlock()
	if err := lease.create(); err != nil {
		return nil, errors.Join(err, lease.Close())
	}
	return lease, nil
}

func agentModeResourceError() error {
	return agentNativeLogError("the owned inspection and capture resources are unavailable or changed")
}
