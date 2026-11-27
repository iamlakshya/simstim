package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A rename through the holder of an older helper (cli-spec 9.5). Such a
// holder goes on running after the helper's upgrade. It answers a rename
// before it saves the name, it never takes the session's lock, and a slow
// copy taken earlier can write an older name back later. So the helper
// takes the lock for it, writes an intent record before sending the
// request, and keeps the authoritative rename state on disk for the
// lifetime of that older holder.

// renameSaveWait is how long a rename waits for an older holder to save
// the name that it took. Such a holder saves each second.
var renameSaveWait = 5 * time.Second

// beforeConfirmRename runs after an older holder answers a rename, before
// confirmRename marks the reply settled and waits for the manifest save.
// Tests simulate an interruption there.
var beforeConfirmRename = func(string) error { return nil }

// unsavedFile is the record, in the session's folder, of the latest rename
// sent to a running older holder.
const unsavedFile = "rename.unsaved"

// unsavedRename is what unsavedFile stores: the name sent to the older
// holder, all candidate names sent while that holder ran, the manifest's
// UpdatedAt before the rename went, and whether a request ended without a
// clear reply from the holder.
type unsavedRename struct {
	Name       string    `json:"name"`
	Candidates []string  `json:"candidates,omitempty"`
	Before     time.Time `json:"before"`
	Uncertain  bool      `json:"uncertain,omitempty"`
}

func unsavedPath(id string) string { return filepath.Join(paths.SessionDir(id), unsavedFile) }

// writeUnsavedFile writes raw bytes to unsavedPath(id). Tests replace it to
// inject a write failure after reading existing rename state.
var writeUnsavedFile = writeUnsavedBytes

// writeUnsaved atomically writes u to unsavedPath(id).
func writeUnsaved(id string, u unsavedRename) error {
	raw, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("marshal rename state: %w", err)
	}
	return writeUnsavedFile(id, raw)
}

// writeUnsavedBytes atomically writes raw to unsavedPath(id).
func writeUnsavedBytes(id string, raw []byte) error {
	dir := paths.SessionDir(id)
	f, err := os.CreateTemp(dir, ".rename-*")
	if err != nil {
		return fmt.Errorf("create rename state: %w", err)
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod rename state: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write rename state: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close rename state: %w", err)
	}
	if err := os.Rename(tmp, unsavedPath(id)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace rename state: %w", err)
	}
	return nil
}

// renameRunning asks a running session's holder to rename it. The holder
// may wait for the session's lock, so the request waits longer than that.
// For an older holder, whose hello does not name renameSaves, the rename
// holds the lock itself, writes the intent record before sending the
// request, and waits until the manifest has the name (confirmRename).
func renameRunning(id, name string) error {
	var unlock func()
	var before time.Time
	var prev []byte
	var hadPrev, wasUncertain bool
	var cands []string
	_, err := holderRequest(id, "rename", map[string]string{"name": name}, renameWait+5*time.Second, func(h proto.Hello) error {
		if slices.Contains(h.Features, renameSaves) {
			return nil
		}
		u, lerr := lockSessionWait(id, renameWait)
		if lerr != nil {
			return lerr
		}
		m, merr := LoadManifest(id)
		if merr != nil {
			u()
			return UserError("not_found", "no session "+id, "List sessions to find its ID.")
		}
		before = m.UpdatedAt
		cands = []string{name}
		if b, rerr := os.ReadFile(unsavedPath(id)); rerr == nil {
			prev, hadPrev = b, true
			var prevU unsavedRename
			if json.Unmarshal(b, &prevU) != nil || prevU.Name == "" {
				u()
				return UserError("busy", "the rename state of session "+id+" could not be read, so it was not renamed",
					"Check disk space and permissions on the host.")
			}
			wasUncertain = prevU.Uncertain
			cands = appendCandidate(appendCandidate(prevU.Candidates, prevU.Name), name)
			if prevU.Uncertain {
				u()
				return UserError("busy",
					"an earlier rename of session "+id+" did not answer in time, so it was not renamed",
					"Wait a moment, or quit the agent and resume the session.")
			}
		} else if !errors.Is(rerr, os.ErrNotExist) {
			u()
			return UserError("save", fmt.Sprintf("could not read the rename state of session %s: %v", id, rerr),
				"Check disk space and permissions on the host.")
		}
		if werr := writeUnsaved(id, unsavedRename{Name: name, Candidates: cands, Before: before, Uncertain: true}); werr != nil {
			u()
			return UserError("save", fmt.Sprintf("could not record the rename of session %s: %v", id, werr),
				"Check disk space and permissions on the host.")
		}
		unlock = u
		return nil
	})
	if unlock == nil {
		return err
	}
	defer unlock()
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) && pe.Code != "exiting" && !wasUncertain {
			if hadPrev {
				_ = writeUnsavedFile(id, prev)
			} else {
				_ = os.Remove(unsavedPath(id))
			}
		}
		return err
	}
	if err := beforeConfirmRename(id); err != nil {
		return err
	}
	if werr := writeUnsaved(id, unsavedRename{Name: name, Candidates: cands, Before: before, Uncertain: wasUncertain}); werr != nil {
		return UserError("save", fmt.Sprintf("could not record the rename of session %s: %v", id, werr),
			"Check disk space and permissions on the host.")
	}
	return confirmRename(id, name, before)
}

func appendCandidate(list []string, name string) []string {
	if name == "" || slices.Contains(list, name) {
		return list
	}
	return append(slices.Clone(list), name)
}

// confirmRename waits, under the session's lock, until the manifest has
// name saved after before, the manifest's time before the rename went. The
// authoritative rename record stays on disk for the lifetime of the older
// holder so a later stale save cannot expose the session under an older
// name.
func confirmRename(id, name string, before time.Time) error {
	deadline := time.Now().Add(renameSaveWait)
	for {
		if m, err := LoadManifest(id); err == nil && m.Name == name && !m.UpdatedAt.Equal(before) {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return UserError("busy", fmt.Sprintf("the holder of session %s took the name %s, but did not save it in time", id, name),
		"Wait a moment, then list the sessions to check the name.")
}

// renameUnsaved reports whether a guarded delete must fail closed because
// an older holder's rename state is uncertain or does not match m. The
// caller holds the session's lock. When no older holder runs, a settled
// rename is reconciled into m and manifest.json before the record goes; an
// uncertain record stays fail-closed until an explicit Rename sets a name.
func renameUnsaved(id string, m *Manifest) bool {
	b, err := os.ReadFile(unsavedPath(id))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false
	case err != nil:
		return true
	}
	var u unsavedRename
	if json.Unmarshal(b, &u) != nil || u.Name == "" || u.Uncertain {
		return true
	}
	if !oldHolderRuns(id) {
		return reconcileUnsavedLocked(m) != nil
	}
	return m.Name != u.Name || m.UpdatedAt.Equal(u.Before)
}

// reconcileUnsavedLocked writes a settled rename.unsaved record into m and
// manifest.json under first.lock once no older holder runs, removing the
// record only after m.Save succeeds. An uncertain record stays untouched.
func reconcileUnsavedLocked(m *Manifest) error {
	b, err := os.ReadFile(unsavedPath(m.ID))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("read rename state of session %s: %w", m.ID, err)
	}
	var u unsavedRename
	if err := json.Unmarshal(b, &u); err != nil || u.Name == "" {
		return fmt.Errorf("corrupt rename state of session %s", m.ID)
	}
	unlockFirst, err := lockFirstSave(m.ID, renameWait)
	if err != nil {
		return err
	}
	defer unlockFirst()
	disk, err := LoadManifest(m.ID)
	if err != nil {
		return err
	}
	if u.Uncertain {
		*m = *disk
		return nil
	}
	if disk.Name != u.Name {
		disk.Name = u.Name
		if err := disk.Save(); err != nil {
			return err
		}
	}
	*m = *disk
	_ = os.Remove(unsavedPath(m.ID))
	return nil
}

// settledRenameName reads an acknowledged name without changing its record.
func settledRenameName(id string) (string, bool) {
	b, err := os.ReadFile(unsavedPath(id))
	if err != nil {
		return "", false
	}
	var u unsavedRename
	if json.Unmarshal(b, &u) != nil || u.Uncertain || u.Name == "" {
		return "", false
	}
	return u.Name, true
}

// reconcileForList applies a settled rename.unsaved record to m for List,
// and reconciles it to manifest.json when no older holder runs.
func reconcileForList(id string, m *Manifest) {
	name, ok := settledRenameName(id)
	if !ok {
		return
	}
	m.Name = name
	if oldHolderRuns(id) {
		return
	}
	unlock, err := lockSession(id)
	if err != nil {
		return
	}
	defer unlock()
	if oldHolderRuns(id) {
		return
	}
	if diskM, err := LoadManifest(id); err == nil {
		_ = reconcileUnsavedLocked(diskM)
	}
}
