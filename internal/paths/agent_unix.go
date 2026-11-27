//go:build !windows

package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/iamlakshya/simstim/internal/brand"
)

func openPrivateRuntimeDir() (*os.Root, error) {
	uid := uint32(os.Getuid())
	base, name := os.TempDir(), fmt.Sprintf("%s-%d", brand.Name, os.Getuid())
	var baseInfo fs.FileInfo
	if xdgDir := os.Getenv("XDG_RUNTIME_DIR"); xdgDir != "" {
		if !filepath.IsAbs(xdgDir) {
			return nil, fmt.Errorf("XDG_RUNTIME_DIR %q must be an absolute path", xdgDir)
		}
		fi, err := os.Lstat(xdgDir) //nolint:gosec // G703: validating the user's XDG_RUNTIME_DIR without following symlinks
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stat XDG_RUNTIME_DIR %s: %w", xdgDir, err)
		}
		if err == nil {
			if err := checkPrivateDirInfo(xdgDir, fi, uid); err != nil {
				return nil, fmt.Errorf("unsafe XDG_RUNTIME_DIR: %w", err)
			}
			base, name = xdgDir, brand.Name
			baseInfo = fi
		}
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		return nil, fmt.Errorf("open runtime base: %w", err)
	}
	defer parent.Close()
	if name == brand.Name {
		fi, err := parent.Stat(".")
		if err != nil {
			return nil, fmt.Errorf("check runtime base: %w", err)
		}
		current, err := os.Lstat(base)
		if err != nil || !os.SameFile(fi, current) || baseInfo == nil || !os.SameFile(fi, baseInfo) {
			return nil, fmt.Errorf("runtime base changed while opening")
		}
		if err := checkPrivateDirInfo(base, fi, uid); err != nil {
			return nil, err
		}
	}
	if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("make runtime folder: %w", err)
	}
	dir := filepath.Join(base, name)
	fi, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("stat runtime folder %s: %w", dir, err)
	}
	if err := checkPrivateDirInfo(dir, fi, uid); err != nil {
		return nil, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open runtime folder %s: %w", dir, err)
	}
	rfi, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("verify runtime folder %s: %w", dir, err)
	}
	if !os.SameFile(fi, rfi) {
		_ = root.Close()
		return nil, fmt.Errorf("runtime folder %s changed while opening", dir)
	}
	if err := checkPrivateDirInfo(dir, rfi, uid); err != nil {
		_ = root.Close()
		return nil, err
	}

	probe := filepath.Join(dir, "agent-2bcdefg3.sock")
	if len(probe) > MaxUnixSocketPath {
		_ = root.Close()
		return nil, fmt.Errorf("runtime folder path %s is too long for Unix sockets (%d > %d bytes)", dir, len(probe), MaxUnixSocketPath)
	}
	return root, nil
}

func checkPrivateDirInfo(path string, fi fs.FileInfo, wantUID uint32) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symlink", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read owner of %s", path)
	}
	if st.Uid != wantUID {
		return fmt.Errorf("%s is owned by uid %d, want %d", path, st.Uid, wantUID)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s has permissions %04o. Group and other access must be clear (0700)", path, perm)
	}
	return nil
}
