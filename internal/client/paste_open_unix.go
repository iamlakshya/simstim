//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openResolved opens resolved, an absolute path with no link in it, one
// folder at a time from the root. It opens each folder relative to the one
// before, without following a link, and checks the identity of the open
// folder against the private folders. It opens the file relative to the
// last folder, again without following a link. So a folder that changes
// after the checks, into a link or into a private folder, is caught by its
// handle, and the upload reads the file that the walk reached.
func openResolved(resolved string, folders []privateFolder) (*os.File, string) {
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(resolved), "/"), "/")
	dir, err := os.Open("/")
	if err != nil {
		return nil, "could not be opened (" + err.Error() + ")"
	}
	for _, name := range parts[:len(parts)-1] {
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = dir.Close()
		if err != nil {
			return nil, "changed while the client checked it (" + err.Error() + ")"
		}
		dir = os.NewFile(uintptr(fd), name)
		if why := privateHandle(dir, folders); why != "" {
			_ = dir.Close()
			return nil, why
		}
	}
	// O_NONBLOCK: a file that became a pipe must not hang the open.
	fd, err := unix.Openat(int(dir.Fd()), parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	_ = dir.Close()
	if err != nil {
		return nil, "changed while the client checked it (" + err.Error() + ")"
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, "could not be opened (" + err.Error() + ")"
	}
	f := os.NewFile(uintptr(fd), resolved)
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, "changed while the client checked it: it is not a regular file"
	}
	return f, ""
}

// privateHandle says whether an open folder is a private folder, or "".
func privateHandle(dir *os.File, folders []privateFolder) string {
	st, err := dir.Stat()
	if err != nil {
		return "could not be checked (" + err.Error() + ")"
	}
	for _, f := range folders {
		if os.SameFile(st, f.info) {
			return "is in ~/" + f.name + ", a private folder"
		}
	}
	return ""
}
