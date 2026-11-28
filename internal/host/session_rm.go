package host

import (
	"fmt"
	"io/fs"
	"os"
)

// removeSessionDir deletes a session directory even when package caches inside
// it contain read-only directories (such as Go's 0555 module cache), without
// ever following symlinks outside dir.
func removeSessionDir(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	root, rerr := os.OpenRoot(dir)
	if rerr != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if p != "." {
				_ = root.Chmod(p, 0o700)
			}
			return nil
		}
		if d.IsDir() && p != "." {
			_ = root.Chmod(p, 0o700)
		}
		return nil
	})
	_ = root.Close()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}
