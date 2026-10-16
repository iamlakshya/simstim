//go:build unix

package diag

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// OpenPlain opens a debug file to read it into a bundle. It does not follow
// a symbolic link, and refuses anything but a regular file with one name: a
// hard link to a key in a debug folder must not ship. It checks the file
// that it opened, so nothing can swap the file between a check and the read.
func OpenPlain(path string) (*os.File, error) {
	// O_NONBLOCK: a pipe in the folder must not hold the open up.
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	st, err := f.Stat()
	if err == nil && !plain(st) {
		err = errors.New("it is not a regular file with one name")
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// plain reports whether st is a regular file with one name.
func plain(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return st.Mode().IsRegular() && ok && sys.Nlink == 1
}
