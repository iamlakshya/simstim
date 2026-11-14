//go:build linux

package guest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Reserved targets are made before any image program runs. No target or
// intermediate component may resolve through an image/session symlink.
func vmDirectory(root *os.Root, name string, mode os.FileMode) error {
	if name == "." {
		return nil
	}
	if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "../") {
		return fmt.Errorf("invalid VM directory target")
	}
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, mode); err != nil {
				return fmt.Errorf("make VM target%s: %w", current, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect VM target%s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("VM target%s is not a real directory", current)
		}
	}
	return nil
}

func vmOwnedDirectory(root *os.Root, name string, mode os.FileMode, uid, gid int, own func(*os.File, int, int) error) error {
	if err := vmDirectory(root, name, mode); err != nil {
		return err
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("VM directory changed during setup")
	}
	if err := own(file, uid, gid); err != nil {
		return fmt.Errorf("own VM directory: %w", err)
	}
	return file.Chmod(mode)
}

func vmAtomicFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(name)
	if err := vmDirectory(root, parent, 0o755); err != nil {
		return err
	}
	dir, err := root.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	leaf := ".vm-new-" + hex.EncodeToString(nonce[:])
	file, err := dir.OpenFile(leaf, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create VM setup file: %w", err)
	}
	defer dir.Remove(leaf)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Chmod(mode)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := dir.Rename(leaf, filepath.Base(name)); err != nil {
		return err
	}
	directory, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
