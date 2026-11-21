//go:build linux

package join

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/join/code"
)

func randomName() string         { return rand.Text() }
func validAccount(s string) bool { return code.ValidAccount(s) }
func fileOwner(st os.FileInfo) (int, int) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(s.Uid), int(s.Gid)
}
func protectedPath(path string, dir bool) error {
	if !cleanAbsolute(path) {
		return codeFailure("join_ledger", "the join record path is not absolute and clean")
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for n, p := range parts {
		current = filepath.Join(current, p)
		st, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("check the root-owned join path: %w", err)
		}
		uid, _ := fileOwner(st)
		if st.Mode()&os.ModeSymlink != 0 || uid != 0 || st.Mode().Perm()&0o022 != 0 {
			return codeFailure("join_ledger", "another user can change a join record or an ancestor")
		}
		if n < len(parts)-1 || dir {
			if !st.IsDir() {
				return codeFailure("join_ledger", "a join record ancestor is not a directory")
			}
		} else if !st.Mode().IsRegular() {
			return codeFailure("join_ledger", "a join record is not a regular file")
		}
	}
	return nil
}
func protectedDir(path string, mode os.FileMode) error {
	if err := protectedPath(filepath.Dir(path), true); err != nil {
		return err
	}
	if err := os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
		return err
	}
	return protectedPath(path, true)
}
func identity(path string) (uint64, uint64, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, codeFailure("join_ledger", "the native file identity is unavailable")
	}
	return uint64(s.Dev), s.Ino, nil
}
