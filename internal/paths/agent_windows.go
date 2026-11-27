//go:build windows

package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func openPrivateRuntimeDir() (*os.Root, error) {
	dir := RuntimeDir()
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("make runtime folder %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat runtime folder %s: %w", dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must not be a symlink", dir)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	root, err := os.OpenRoot(dir)
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
	return root, nil
}
