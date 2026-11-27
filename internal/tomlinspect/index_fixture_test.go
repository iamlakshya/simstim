//go:build darwin || linux

package tomlinspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// This injected lease is TEST ONLY. Production has only the actual client
// mode factory; no path/provider fallback is installed in Inspector.
type fixtureIndexFactory struct {
	root           string
	gate           chan struct{}
	workspace      [IndexWorkspaceBytes]byte
	opened, closed int
	onClose        func(*os.File, []byte) error
}
type fixtureIndexLease struct {
	factory *fixtureIndexFactory
	ctx     context.Context
	file    *os.File
	closed  bool
}
type fixtureIndexView struct{ lease *fixtureIndexLease }

func fixtureIndex(t *testing.T) *fixtureIndexFactory {
	t.Helper()
	root := t.TempDir()
	for _, item := range []struct{ name, leaf string }{
		{"HOME", "home"}, {"USERPROFILE", "profile"}, {"XDG_CONFIG_HOME", "config"}, {"XDG_STATE_HOME", "state"}, {"XDG_DATA_HOME", "data"}, {"XDG_CACHE_HOME", "cache"}, {"XDG_RUNTIME_DIR", "run"}, {"XDG_CONFIG_DIRS", "config-dirs"}, {"XDG_DATA_DIRS", "data-dirs"},
	} {
		path := filepath.Join(root, item.leaf)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(item.name, path)
	}
	return &fixtureIndexFactory{root: root, gate: make(chan struct{}, 1)}
}
func (f *fixtureIndexFactory) AcquireIndex(ctx context.Context, size int64) (IndexLease, error) {
	select {
	case f.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	keep := false
	defer func() {
		if !keep {
			<-f.gate
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(f.root, "fixture-index")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, ErrIndexInvariant
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		_ = file.Close()
		return nil, ErrIndexInvariant
	}
	if err := os.Remove(path); err != nil {
		_ = file.Close()
		return nil, err
	}
	f.opened++
	keep = true
	return &fixtureIndexLease{factory: f, ctx: ctx, file: file}, nil
}
func (l *fixtureIndexLease) File() IndexFile   { return fixtureIndexView{lease: l} }
func (l *fixtureIndexLease) Workspace() []byte { return l.factory.workspace[:] }
func (l *fixtureIndexLease) Close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	err := l.ctx.Err()
	if l.factory.onClose != nil {
		err = errors.Join(err, l.factory.onClose(l.file, l.factory.workspace[:]))
	}
	err = errors.Join(err, l.file.Close())
	clear(l.factory.workspace[:])
	l.factory.closed++
	<-l.factory.gate
	return err
}
func (v fixtureIndexView) allowed() error {
	if v.lease.closed {
		return ErrIndexInvariant
	}
	return v.lease.ctx.Err()
}
func (v fixtureIndexView) ReadAt(data []byte, offset int64) (int, error) {
	if err := v.allowed(); err != nil {
		return 0, err
	}
	return v.lease.file.ReadAt(data, offset)
}
func (v fixtureIndexView) WriteAt(data []byte, offset int64) (int, error) {
	if err := v.allowed(); err != nil {
		return 0, err
	}
	return v.lease.file.WriteAt(data, offset)
}
func (v fixtureIndexView) Stat() (os.FileInfo, error) {
	if err := v.allowed(); err != nil {
		return nil, err
	}
	return v.lease.file.Stat()
}

type syntaxOnlyVisitor struct{}

func (syntaxOnlyVisitor) EnterTable(PathCursor, TableCursor) error { return nil }
func (syntaxOnlyVisitor) LeaveTable(PathCursor) error              { return nil }
func (syntaxOnlyVisitor) EnterArray(PathCursor, ArrayCursor) error { return ErrSyntax }
func (syntaxOnlyVisitor) LeaveArray(PathCursor) error              { return ErrSyntax }
func (syntaxOnlyVisitor) Scalar(PathCursor, ScalarCursor) error    { return ErrSyntax }
func (syntaxOnlyVisitor) Finish() error                            { return nil }
