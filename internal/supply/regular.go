package supply

import (
	"fmt"
	"os"
	"path/filepath"
)

// openRegular refuses special files before opening them. The second check
// binds the open descriptor to the regular file that was inspected.
func openRegular(p string) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return nil, fmt.Errorf("open the input folder of %s: %w", p, err)
	}
	defer root.Close()
	name := filepath.Base(p)
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	f, err := openReadOnly(root, name)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat the open file %s: %w", p, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, fmt.Errorf("%s changed while opening a regular file", p)
	}
	return f, nil
}
