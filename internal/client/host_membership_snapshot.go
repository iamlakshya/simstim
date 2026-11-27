package client

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type hostMembershipSnapshot struct {
	mu               sync.RWMutex
	closed           bool
	root             *os.Root
	rootPath         string
	rootInfo         os.FileInfo
	file             *os.File
	info             os.FileInfo
	path, generation string
}

func openHostMembershipSnapshot(ctx context.Context) (*hostMembershipSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := hostsPath()
	dir := filepath.Dir(path)
	before, err := os.Lstat(dir)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, hostStateRemovalError("the current host membership root cannot be verified")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|grantsStateNoFollow|grantsStateNonblock, 0)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	info, err := cachefs.ValidateFile(root, filepath.Base(path), file, 0600)
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, err
	}
	generation, err := readMembershipGeneration(ctx, file)
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, err
	}
	return &hostMembershipSnapshot{root: root, rootPath: dir, rootInfo: before, file: file, info: info, path: path, generation: generation}, nil
}
func (s *hostMembershipSnapshot) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	_ = s.file.Close()
	_ = s.root.Close()
}
func (s *hostMembershipSnapshot) check(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return hostStateRemovalError("the verified membership snapshot ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path != hostsPath() {
		return hostStateRemovalError("the host membership location changed")
	}
	dir, err := os.Lstat(s.rootPath)
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 || !os.SameFile(dir, s.rootInfo) {
		return hostStateRemovalError("the host membership root changed")
	}
	info, err := cachefs.ValidateFile(s.root, filepath.Base(s.path), s.file, 0600)
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil || !stateSnapshotSame(s.info, info) {
		return hostStateRemovalError("host membership changed after the verified original forget")
	}
	generation, err := readMembershipGeneration(ctx, s.file)
	if err != nil {
		return err
	}
	if generation != s.generation {
		return hostStateRemovalError("the current host membership generation changed")
	}
	return nil
}
