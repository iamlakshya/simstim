//go:build linux

package join

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func safeHome(home string, uid int) error {
	if !cleanAbsolute(home) || home == "/" {
		return codeFailure("join_account", "the account home is unsafe")
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(home, "/"), "/")
	for n, p := range parts {
		current = filepath.Join(current, p)
		st, err := os.Lstat(current)
		if err != nil {
			return err
		}
		owner, _ := fileOwner(st)
		allowed := owner == 0
		if n == len(parts)-1 {
			allowed = allowed || owner == uid
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !allowed || st.Mode().Perm()&0o022 != 0 {
			return codeFailure("join_account", "the account home or an ancestor has unsafe ownership or permissions")
		}
	}
	return nil
}
func openKeyScope(home string, uid int) (*os.Root, error) {
	if err := safeHome(home, uid); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	st, err := root.Lstat(".ssh")
	if err != nil {
		return nil, err
	}
	owner, _ := fileOwner(st)
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || owner != uid || st.Mode().Perm()&0o022 != 0 {
		return nil, codeFailure("join_key", "the account's .ssh directory is unsafe")
	}
	return root.OpenRoot(".ssh")
}
func snapshotScopedFile(root *os.Root, name string) (fileState, error) {
	var s fileState
	st, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return s, codeFailure("join_file", "the authorised keys are not a bounded regular file")
	}
	uid, gid := fileOwner(st)
	f, err := root.Open(name)
	if err != nil {
		return s, err
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return s, err
	}
	if len(b) > 1<<20 {
		return s, codeFailure("join_file", "the authorised keys grew while being read")
	}
	return fileState{true, b, uint32(st.Mode().Perm()), uid, gid}, nil
}
func (e *linuxExecutor) applyKey(ctx context.Context, home string, uid, gid int, line string) error {
	root, err := openKeyScope(home, uid)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := snapshotScopedFile(root, "authorized_keys")
	if err != nil {
		return err
	}
	entry := e.ledger.entry("key")
	if entry == nil {
		for _, l := range strings.Split(string(current.Data), "\n") {
			if l == line {
				return nil
			}
		}
		data := append([]byte(nil), current.Data...)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, []byte(line+"\n")...)
		entry, err = e.ahead(ledgerEntry{ID: "key", Kind: "file", Path: filepath.Join(home, ".ssh", "authorized_keys"), Home: home, UID: uid, Before: current, After: fileState{true, data, 0o600, uid, gid}})
		if err != nil {
			return err
		}
	}
	if entry.Home != home || entry.UID != uid {
		return codeFailure("join_changed", "the original key account changed")
	}
	if sameFileState(current, entry.After) {
		return e.applied(entry)
	}
	if !sameFileState(current, entry.Before) {
		return codeFailure("join_changed", "the authorised keys changed during an incomplete join")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = durableScopedFile(root, "authorized_keys", entry.After.Data, 0o600, uid, gid); err != nil {
		return err
	}
	return e.applied(entry)
}
func restoreKey(entry *ledgerEntry) error {
	root, err := openKeyScope(entry.Home, entry.UID)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := snapshotScopedFile(root, "authorized_keys")
	if err != nil {
		return err
	}
	if sameFileState(current, entry.Before) {
		return nil
	}
	if !sameFileState(current, entry.After) {
		return codeFailure("join_changed", "the authorised keys changed after the join; the undo left them in place")
	}
	if !entry.Before.Exists {
		return root.Remove("authorized_keys")
	}
	return durableScopedFile(root, "authorized_keys", entry.Before.Data, os.FileMode(entry.Before.Mode), entry.Before.UID, entry.Before.GID)
}
