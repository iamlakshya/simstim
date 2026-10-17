//go:build !unix

package diag

import (
	"errors"
	"fmt"
	"os"
)

// OpenPlain opens a debug file to read it into a bundle, and refuses
// anything but a regular file. Here a link cannot be told apart, so it
// checks the name before it opens it, and the file after.
func OpenPlain(path string) (*os.File, error) {
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("open %s: it is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("open %s: %w", path, errors.New("it is not a regular file"))
	}
	return f, nil
}
