package join

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type fileState struct {
	Exists bool   `json:"exists"`
	Data   []byte `json:"data,omitempty"`
	Mode   uint32 `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
}
type journalIdentity struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Device uint64 `json:"device,omitempty"`
	Inode  uint64 `json:"inode,omitempty"`
}
type ledgerEntry struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	State    string            `json:"state"`
	Path     string            `json:"path,omitempty"`
	Before   fileState         `json:"before"`
	After    fileState         `json:"after"`
	UID      int               `json:"uid,omitempty"`
	GID      int               `json:"gid,omitempty"`
	Home     string            `json:"home,omitempty"`
	Origin   string            `json:"origin,omitempty"`
	Device   uint64            `json:"device,omitempty"`
	Inode    uint64            `json:"inode,omitempty"`
	Script   string            `json:"script,omitempty"`
	Undo     string            `json:"undo,omitempty"`
	Journals []journalIdentity `json:"journals,omitempty"`
}
type ledger struct {
	Version  int           `json:"version"`
	Account  string        `json:"account"`
	Nonce    uint64        `json:"nonce"`
	KeyHash  string        `json:"key_hash"`
	Entries  []ledgerEntry `json:"entries"`
	Complete bool          `json:"complete"`
}

func digest(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func (l *ledger) entry(id string) *ledgerEntry {
	for n := range l.Entries {
		if l.Entries[n].ID == id {
			return &l.Entries[n]
		}
	}
	return nil
}
func readLedger(path string) (*ledger, error) {
	if err := protectedPath(path, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	d.DisallowUnknownFields()
	var l ledger
	if err = d.Decode(&l); err != nil {
		return nil, fmt.Errorf("read the root-owned join ledger: %w", err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, codeFailure("join_ledger", "the join ledger has trailing data")
	}
	if l.Version != 1 || !validLedger(&l) {
		return nil, codeFailure("join_ledger", "the join ledger is invalid")
	}
	return &l, nil
}
func validLedger(l *ledger) bool {
	if !validAccount(l.Account) || l.Nonce == 0 || len(l.KeyHash) != 64 || len(l.Entries) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, e := range l.Entries {
		if seen[e.ID] || e.ID == "" || (e.State != "prepared" && e.State != "applied" && e.State != "undone") {
			return false
		}
		seen[e.ID] = true
		switch e.Kind {
		case "file", "directory", "account", "group", "root":
		default:
			return false
		}
	}
	return true
}
func writeLedger(dir string, l *ledger) error {
	if !validLedger(l) {
		return codeFailure("join_ledger", "refusing to write an invalid join ledger")
	}
	if err := protectedPath(dir, true); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return durableFile(filepath.Join(dir, "ledger.json"), b, 0o600, 0, 0)
}
func durableFile(path string, b []byte, mode os.FileMode, uid, gid int) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return durableScopedFile(root, filepath.Base(path), b, mode, uid, gid)
}
func durableScopedFile(root *os.Root, name string, b []byte, mode os.FileMode, uid, gid int) error {
	f, err := root.OpenFile(".join-"+randomName(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	tmp := filepath.Base(f.Name())
	defer root.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = root.Rename(tmp, name); err != nil {
		return err
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
func sameFileState(a, b fileState) bool {
	return a.Exists == b.Exists && (!a.Exists || a.Mode == b.Mode && a.UID == b.UID && a.GID == b.GID && bytes.Equal(a.Data, b.Data))
}
func snapshotFile(path string) (fileState, error) {
	var s fileState
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return s, codeFailure("join_file", "a join target is not a bounded regular file")
	}
	uid, gid := fileOwner(st)
	s = fileState{Exists: true, Mode: uint32(st.Mode().Perm()), UID: uid, GID: gid}
	s.Data, err = os.ReadFile(path)
	return s, err
}
func restoreFile(e *ledgerEntry) error {
	current, err := snapshotFile(e.Path)
	if err != nil {
		return err
	}
	if sameFileState(current, e.Before) {
		return nil
	}
	if !sameFileState(current, e.After) {
		return codeFailure("join_changed", "a file changed after the join; the undo left it in place")
	}
	if !e.Before.Exists {
		return os.Remove(e.Path)
	}
	return durableFile(e.Path, e.Before.Data, os.FileMode(e.Before.Mode), e.Before.UID, e.Before.GID)
}
func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}
