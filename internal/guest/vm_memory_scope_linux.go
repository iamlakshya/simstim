//go:build linux

package guest

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/nativememory"
	"golang.org/x/sys/unix"
)

// vmMemoryScope retains native facts from the actual fixed init's newly
// mounted hierarchy. It is not original boot/Need authority or admission.
// Only the existing verified boot owner can supply that separate receipt.
// The runtime cancels and joins all users before closing these descriptors.
type vmMemoryScope struct {
	mu       sync.Mutex
	current  *nativememory.Current
	root     *os.File
	closed   bool
	closeErr error
}

// openVMMemoryScope follows setupVMMemory's actual fresh cgroup2 mount in
// the verified VM init. No path, PID, limit, provider or trusted flag is input.
func openVMMemoryScope(ctx context.Context) (_ *vmMemoryScope, returned error) {
	if ctx == nil || os.Getpid() != 1 || os.Geteuid() != 0 {
		return nil, vmError("native VM memory facts require the actual fixed PID1 init")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open("/sys/fs/cgroup", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	scope := &vmMemoryScope{root: os.NewFile(uintptr(fd), "VM memory hierarchy root")}
	if scope.root == nil {
		return nil, errors.Join(vmError("the VM memory hierarchy descriptor is unavailable"), unix.Close(fd))
	}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, scope.close())
		}
	}()
	scope.current, err = nativememory.CaptureCurrent(ctx)
	if err != nil {
		return nil, err
	}
	if err := scope.current.MatchHierarchyRoot(ctx, scope.root); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scope, nil
}

func (s *vmMemoryScope) close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.current != nil {
		s.closeErr = errors.Join(s.closeErr, s.current.Close())
		s.current = nil
	}
	if s.root != nil {
		s.closeErr = errors.Join(s.closeErr, s.root.Close())
		s.root = nil
	}
	return s.closeErr
}
