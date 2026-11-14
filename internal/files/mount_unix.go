//go:build linux || darwin

package files

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// PinMount opens every path component without following a link and returns
// the final owned descriptor. Writable grants are checked on those same
// descriptors for Git metadata in ancestors and inside the granted tree.
func PinMount(name string, write bool) (*os.File, error) {
	if !strings.HasPrefix(name, "/") || name == "/" || !textPath(name) {
		return nil, mountError("bad_mount", "a mount source must be an absolute folder path")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return nil, mountError("bad_mount", "a mount source must be clean")
		}
		if write && gitAt(fd) {
			unix.Close(fd)
			return nil, mountError("mount_repo_rw", "a writable mount cannot contain a Git repository")
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		unix.Close(fd)
		if e != nil {
			return nil, mountError("bad_mount", "the mount source cannot be opened without following links")
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), "mount-source")
	st, err := f.Stat()
	if err != nil || !st.IsDir() {
		f.Close()
		return nil, mountError("bad_mount", "a mount source must be a folder")
	}
	if write {
		repo, err := containsGit(f)
		if err != nil || repo {
			f.Close()
			return nil, mountError("mount_repo_rw", "a writable mount cannot contain a Git repository")
		}
	}
	return f, nil
}

func gitAt(fd int) bool {
	var st unix.Stat_t
	if unix.Fstatat(fd, ".git", &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		return true
	}
	// Bare repositories have no .git entry. Their own HEAD, object store
	// and refs are enough to refuse a writable grant conservatively.
	var head, objects, refs unix.Stat_t
	return unix.Fstatat(fd, "HEAD", &head, unix.AT_SYMLINK_NOFOLLOW) == nil && head.Mode&unix.S_IFMT != unix.S_IFDIR && unix.Fstatat(fd, "objects", &objects, unix.AT_SYMLINK_NOFOLLOW) == nil && unix.Fstatat(fd, "refs", &refs, unix.AT_SYMLINK_NOFOLLOW) == nil
}

func containsGit(dir *os.File) (bool, error) {
	if gitAt(int(dir.Fd())) {
		return true, nil
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			return true, nil
		}
		if !entry.IsDir() {
			continue
		}
		fd, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect mounted folder: %w", err)
		}
		child := os.NewFile(uintptr(fd), "mount-child")
		repo, err := containsGit(child)
		child.Close()
		if err != nil || repo {
			return repo, err
		}
	}
	return false, nil
}
