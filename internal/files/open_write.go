package files

import (
	"errors"
	"fmt"
	"os"
)

// OpenRegularWrite opens an EXISTING ordinary file read/write through the SAME
// native no-follow component reader. It never creates or truncates a file.
func OpenRegularWrite(root *os.Root, name string) (*os.File, error) {
	if !relativePath(name) {
		return nil, fmt.Errorf("unsafe relative file path")
	}
	file, err := openRegularWriteFile(root, name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("file is not regular"), err, file.Close())
	}
	return file, nil
}
