package client

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

type stateFileView struct {
	ReaderAt io.ReaderAt
	Size     int64
	Exists   bool
}
type stateFileReplace func(context.Context, io.Writer) error

// stateFileTxn owns one canonical mutex, root, lock and source descriptor.
// Its descriptor view cannot outlive the callback. Nothing unlinks its lock.
type stateFileTxn struct {
	ctx       context.Context
	name      string
	root      *os.Root
	anchors   []stateRootAnchor
	lock      *stateFileLock
	source    *os.File
	before    os.FileInfo
	output    *os.File
	removed   bool
	authority func() error
	reader    *stateFDView
}

func withStateFileFD(ctx context.Context, subdirs []string, name string, write bool, change func(stateFileView) (stateFileReplace, error)) error {
	return withStateTransaction(ctx, subdirs, name, write, false, func(tx *stateFileTxn) error {
		replace, err := change(tx.view())
		if errors.Is(err, errStateNoChange) {
			return nil
		}
		if err != nil {
			return err
		}
		if !write || replace == nil {
			return nil
		}
		err = tx.replace(replace)
		var callback *stateCallbackError
		if errors.As(err, &callback) {
			if err != callback {
				return err
			}
			if errors.Is(callback.err, errStateNoChange) {
				return nil
			}
			return callback.err
		}
		return wrapGrantStateError(name, err)
	})
}

func withStateTransaction(ctx context.Context, subdirs []string, name string, create, existingLock bool, run func(*stateFileTxn) error) (result error) {
	ctx, releaseDomain, err := acquireHostStateDomain(ctx, subdirs, name, create, existingLock)
	if err != nil {
		return err
	}
	defer releaseDomain()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stateContextAuthority(ctx); err != nil {
		return err
	}
	releaseScope, err := stateScopeEnter(ctx)
	if err != nil {
		return err
	}
	defer releaseScope()
	if !validStateComponent(name) {
		return badGrantStateError(name)
	}
	unlock, err := lockStateProcess(ctx, stateProcessMutex(subdirs, name))
	if err != nil {
		return err
	}
	defer unlock()
	if err := stateContextAuthority(ctx); err != nil {
		return err
	}
	r, anchors, err := openStateDirRootContext(ctx, subdirs, name, create)
	if stateRootMissing(err, anchors) && !create {
		return (&stateFileTxn{ctx: ctx, name: name, anchors: anchors}).apply(run, false)
	}
	if err != nil {
		return wrapGrantStateError(name, err)
	}
	defer func() {
		if err := closeStateOwnedRoot(ctx, r); result == nil && err != nil {
			result = wrapGrantStateError(name, err)
		}
	}()
	held, err := acquireStateFileLock(ctx, r, name, !existingLock)
	if errors.Is(err, os.ErrNotExist) && existingLock {
		if _, missing := r.Lstat(name); !errors.Is(missing, os.ErrNotExist) {
			return badGrantStateError(name)
		}
		return (&stateFileTxn{ctx: ctx, name: name, root: r, anchors: anchors}).apply(run, false)
	}
	if err != nil {
		return wrapGrantStateError(name, err)
	}
	defer func() {
		if err := held.closeOwned(ctx); result == nil && err != nil {
			result = wrapGrantStateError(name, err)
		}
	}()
	return (&stateFileTxn{ctx: ctx, name: name, root: r, anchors: anchors, lock: held}).apply(run, true)
}

func (tx *stateFileTxn) apply(run func(*stateFileTxn) error, open bool) (result error) {
	defer func() {
		tx.revokeReader()
		if tx.output != nil {
			if err := closeStateOwnedFile(tx.ctx, tx.output); result == nil && err != nil {
				result = wrapGrantStateError(tx.name, err)
			}
		}
		if tx.source != nil {
			if err := closeStateOwnedFile(tx.ctx, tx.source); result == nil && err != nil {
				result = wrapGrantStateError(tx.name, err)
			}
		}
		if result == nil && tx.ctx.Err() != nil {
			result = tx.ctx.Err()
		}
	}()
	if open {
		if err := tx.openSource(); err != nil {
			return wrapGrantStateError(tx.name, err)
		}
	}
	if err := tx.check(); err != nil {
		return wrapGrantStateError(tx.name, err)
	}
	err := run(tx)
	if err != nil {
		return err
	}
	return wrapGrantStateError(tx.name, tx.check())
}

func (tx *stateFileTxn) revokeReader() {
	if tx.reader != nil {
		tx.reader.revoke()
	}
}

func lockStateProcess(ctx context.Context, mu *sync.Mutex) (func(), error) {
	timer := time.NewTimer(hostsWait)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !mu.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, &UserError{Msg: "another command kept the local grant state busy", Hint: "End that command, then try again.", Code: "grant_busy"}
		case <-tick.C:
		}
	}
	if err := ctx.Err(); err != nil {
		mu.Unlock()
		return nil, err
	}
	return mu.Unlock, nil
}

func (tx *stateFileTxn) openSource() error {
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	before, err := tx.root.Lstat(tx.name)
	if stopped := tx.ctx.Err(); stopped != nil {
		return stopped
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return badGrantStateError(tx.name)
	}
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	f, err := tx.root.OpenFile(tx.name, os.O_RDONLY|grantsStateNoFollow|grantsStateNonblock, 0)
	if stopped := tx.ctx.Err(); stopped != nil {
		if f != nil {
			_ = f.Close()
		}
		return stopped
	}
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if stopped := tx.ctx.Err(); stopped != nil {
		_ = f.Close()
		return stopped
	}
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = f.Close()
		return badGrantStateError(tx.name)
	}
	if err := stateRegularMatchesContext(tx.ctx, tx.root, tx.name, f); err != nil {
		_ = f.Close()
		return err
	}
	tx.source, tx.before = f, opened
	return nil
}

func (tx *stateFileTxn) view() stateFileView {
	if tx.reader == nil {
		tx.reader = &stateFDView{ctx: tx.ctx}
		if tx.source != nil {
			tx.reader.r = tx.source
		}
	}
	size := int64(0)
	if tx.before != nil {
		size = tx.before.Size()
	}
	return stateFileView{ReaderAt: tx.reader, Size: size, Exists: tx.source != nil}
}

func (tx *stateFileTxn) check() error {
	if err := stateContextAuthority(tx.ctx); err != nil {
		return err
	}
	if tx.authority != nil {
		if err := tx.authority(); err != nil {
			return err
		}
	}
	return tx.checkSnapshot(tx.ctx)
}

func (tx *stateFileTxn) checkSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stateRootAnchorsMatch(ctx, tx.root, tx.anchors, tx.name); err != nil {
		return err
	}
	if tx.root == nil {
		return ctx.Err()
	}
	if tx.lock == nil {
		if _, err := tx.root.Lstat(tx.name + ".lock"); !errors.Is(err, os.ErrNotExist) {
			return badGrantStateError(tx.name + ".lock")
		}
	}
	if tx.lock != nil {
		if err := tx.lock.check(ctx, tx.root); err != nil {
			return err
		}
	}
	if tx.source != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		after, err := tx.source.Stat()
		if stopped := ctx.Err(); stopped != nil {
			return stopped
		}
		if err != nil || !stateSnapshotSame(tx.before, after) {
			return badGrantStateError(tx.name)
		}
	}
	switch {
	case tx.output != nil:
		if err := stateRegularMatchesContext(ctx, tx.root, tx.name, tx.output); err != nil {
			return err
		}
	case tx.removed || tx.source == nil:
		if _, err := tx.root.Lstat(tx.name); !errors.Is(err, os.ErrNotExist) {
			return badGrantStateError(tx.name)
		}
	default:
		if err := stateRegularMatchesContext(ctx, tx.root, tx.name, tx.source); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func stateSnapshotSame(a, b os.FileInfo) bool {
	return a != nil && b != nil && b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

// stateFDView exposes only ReaderAt. Revocation drains in-flight reads before
// the owned descriptor closes, so retained interfaces cannot read reused FDs.
type stateFDView struct {
	mu      sync.RWMutex
	ctx     context.Context
	r       io.ReaderAt
	revoked bool
}

func (r *stateFDView) ReadAt(b []byte, off int64) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.revoked {
		return 0, os.ErrClosed
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.r == nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if off < 0 {
			return 0, os.ErrInvalid
		}
		if len(b) == 0 {
			return 0, nil
		}
		return 0, io.EOF
	}
	n, err := r.r.ReadAt(b, off)
	if stopped := r.ctx.Err(); stopped != nil {
		return n, stopped
	}
	return n, err
}
func (r *stateFDView) revoke() { r.mu.Lock(); r.revoked = true; r.r = nil; r.mu.Unlock() }

func stateRootAnchorsMatch(ctx context.Context, root *os.Root, anchors []stateRootAnchor, name string) error {
	for _, anchor := range anchors {
		if err := ctx.Err(); err != nil {
			return err
		}
		now, err := os.Lstat(anchor.path)
		if stopped := ctx.Err(); stopped != nil {
			return stopped
		}
		if anchor.info == nil {
			if !errors.Is(err, os.ErrNotExist) {
				return badGrantStateError(name)
			}
			continue
		}
		if err != nil || !statePrivateDirectory(now) || !os.SameFile(anchor.info, now) || now.Mode() != anchor.info.Mode() {
			return badGrantStateError(name)
		}
	}
	if root == nil {
		return ctx.Err()
	}
	if len(anchors) == 0 {
		return badGrantStateError(name)
	}
	if err := stateDirMatchesContext(ctx, root, anchors[len(anchors)-1].info); err != nil {
		return err
	}
	return ctx.Err()
}
