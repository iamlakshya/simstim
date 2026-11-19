package filecopy

import (
	"errors"
	"os"
)

// OpenDirectory pins an actual directory without following any link component.
func OpenDirectory(root *os.Root, relative string) (*os.File, error) {
	if root == nil {
		return nil, failure("cp_path", "the selected root is missing")
	}
	if relative != "." {
		if err := Relative(relative); err != nil {
			return nil, err
		}
	}
	held, err := OpenDirectoryRoot(root, relative, false)
	if err != nil {
		return nil, err
	}
	file, err := held.Open(".")
	closeErr := held.Close()
	if closeErr != nil {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		return nil, errors.Join(err, closeErr)
	}
	return file, err
}
