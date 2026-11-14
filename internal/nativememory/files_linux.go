//go:build linux

package nativememory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type nativeFile struct {
	file    *os.File
	info    os.FileInfo
	fsType  int64
	mountID uint64
}

func inspectNative(file *os.File, filesystem int64, directory bool) (*nativeFile, error) {
	if file == nil {
		return nil, ErrUnknown
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() != directory || !directory && !info.Mode().IsRegular() {
		return nil, ErrUnknown
	}
	var filesystemInfo unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &filesystemInfo); err != nil {
		return nil, err
	}
	if int64(filesystemInfo.Type) != filesystem {
		return nil, fmt.Errorf("native memory filesystem role changed: %w", ErrUnknown)
	}
	var identity unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_TYPE|unix.STATX_INO|unix.STATX_MNT_ID, &identity); err != nil {
		return nil, err
	}
	if identity.Mask&unix.STATX_MNT_ID == 0 || identity.Mnt_id == 0 {
		return nil, ErrUnsupported
	}
	return &nativeFile{file: file, info: info, fsType: filesystem, mountID: identity.Mnt_id}, nil
}

func (f *nativeFile) check() error {
	if f == nil || f.file == nil {
		return ErrUnknown
	}
	fresh, err := inspectNative(f.file, f.fsType, f.info.IsDir())
	if err != nil {
		return err
	}
	if !os.SameFile(f.info, fresh.info) || f.info.Mode() != fresh.info.Mode() || f.mountID != fresh.mountID {
		return fmt.Errorf("native memory descriptor generation changed: %w", ErrUnknown)
	}
	return nil
}

func wrapNative(fd int, role string, filesystem int64, directory bool) (*nativeFile, error) {
	file := os.NewFile(uintptr(fd), role)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrUnknown
	}
	owned, err := inspectNative(file, filesystem, directory)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return owned, nil
}

func openAbsoluteDirectory(ctx context.Context, path string, filesystem int64) (*nativeFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !closedNativePath(path) {
		return nil, ErrUnknown
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, unix.Close(fd))
		}
		if component == "" {
			continue
		}
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		closeErr := unix.Close(fd)
		if err != nil || closeErr != nil {
			if err == nil {
				_ = unix.Close(next)
			}
			return nil, errors.Join(err, closeErr)
		}
		fd = next
	}
	return wrapNative(fd, "native memory directory", filesystem, true)
}

func openChildDirectory(parent *nativeFile, name string, kernelSelf bool) (*nativeFile, error) {
	if err := parent.check(); err != nil {
		return nil, err
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, ErrUnknown
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if kernelSelf {
		if parent.fsType != unix.PROC_SUPER_MAGIC || name != "self" {
			return nil, ErrUnknown
		}
		flags &^= unix.O_NOFOLLOW // Only the fixed kernel-generated self link.
	}
	fd, err := unix.Openat(int(parent.file.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	child, err := wrapNative(fd, "native current memory directory", parent.fsType, true)
	if err != nil {
		return nil, err
	}
	if child.mountID != parent.mountID {
		return nil, errors.Join(ErrUnknown, child.file.Close())
	}
	return child, nil
}

func openAccountingFile(parent *nativeFile, name string) (*nativeFile, error) {
	if err := parent.check(); err != nil {
		return nil, err
	}
	if !accountingFileName(name, parent.fsType) {
		return nil, ErrUnknown
	}
	fd, err := unix.Openat(int(parent.file.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file, err := wrapNative(fd, "native "+name, parent.fsType, false)
	if err != nil {
		return nil, err
	}
	if file.mountID != parent.mountID {
		return nil, errors.Join(ErrUnknown, file.file.Close())
	}
	return file, nil
}

func accountingFileName(name string, filesystem int64) bool {
	if filesystem == unix.PROC_SUPER_MAGIC {
		return name == "meminfo" || name == "cgroup" || name == "mountinfo"
	}
	if filesystem != unix.CGROUP2_SUPER_MAGIC {
		return false
	}
	switch name {
	case "memory.max", "memory.current", "cgroup.controllers", "cgroup.subtree_control", "cgroup.type", "cgroup.procs":
		return true
	default:
		return false
	}
}

func readAccountingBytes(ctx context.Context, parent *nativeFile, name string) (data []byte, failure error) {
	file, err := openAccountingFile(parent, name)
	if err != nil {
		return nil, err
	}
	defer func() { failure = errors.Join(failure, file.file.Close()) }()
	data, err = io.ReadAll(contextReader{ctx: ctx, in: file.file})
	return data, errors.Join(err, file.check(), ctx.Err())
}

func closedNativePath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.ContainsRune(path, 0) || path != "/" && strings.HasSuffix(path, "/") {
		return false
	}
	if path == "/" {
		return true
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
