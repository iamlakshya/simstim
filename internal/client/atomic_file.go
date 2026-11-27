package client

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeAtomicFile gives readers the whole old file or the whole new file.
// A unique, private temporary file prevents concurrent writers sharing a
// truncation target. The caller owns any read, change and write lock.
func writeAtomicFile(path, pattern string, data []byte) error {
	return writeAtomicFileWithHooks(path, pattern, data, atomicFileHooks{})
}

// atomicFileHooks bind optional scoped ownership to an actual publication.
// Existing atomic writes use no hooks and retain their original behaviour.
type atomicFileHooks struct {
	beforeReplace func(*os.Root, string) error
	published     func(os.FileInfo)
}

func writeAtomicFileWithHooks(path, pattern string, data []byte, hooks atomicFileHooks) error {
	dir := filepath.Dir(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer root.Close()
	base := filepath.Base(path)
	if !filepath.IsLocal(base) || base == "." {
		return fmt.Errorf("invalid atomic file name %q", base)
	}
	tmp := strings.TrimSuffix(pattern, "*") + rand.Text()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("make a file in %s: %w", dir, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	var created os.FileInfo
	if hooks.published != nil || hooks.beforeReplace != nil {
		created, err = f.Stat()
		if err != nil {
			return fmt.Errorf("identify the atomic file in %s: %w", dir, err)
		}
	}
	defer cleanupAtomicTemp(root, tmp, created)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if created == nil {
		closeErr := f.Close()
		closed = true
		if err == nil {
			err = closeErr
		}
	}
	if err == nil && hooks.beforeReplace != nil {
		err = hooks.beforeReplace(root, base)
	}
	if err == nil && created != nil {
		if !sameAtomicFile(root, tmp, created) {
			err = fmt.Errorf("the atomic file in %s changed before publication", dir)
		}
	}
	if err == nil {
		err = root.Rename(tmp, base)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if hooks.published != nil {
		if !sameAtomicFile(root, base, created) {
			return fmt.Errorf("the atomic file %s changed at publication", path)
		}
		hooks.published(created)
	}
	if !closed {
		closeErr := f.Close()
		closed = true
		if closeErr != nil {
			return fmt.Errorf("close the published file %s: %w", path, closeErr)
		}
	}
	return nil
}

func sameAtomicFile(root *os.Root, name string, expected os.FileInfo) bool {
	current, err := root.Lstat(name)
	return err == nil && expected != nil && os.SameFile(expected, current)
}

func cleanupAtomicTemp(root *os.Root, name string, created os.FileInfo) {
	if created == nil || sameAtomicFile(root, name, created) {
		_ = root.Remove(name)
	}
}
