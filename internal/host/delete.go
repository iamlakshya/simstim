package host

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Deleting a session: its holder stops, its files go, and its worktree
// when that loses nothing. A client may bind the delete to the name that
// the user confirmed.

// Delete stops a session and removes its files, and its worktree when that loses nothing.
// Its name does not count.
func Delete(id string) (DeleteResult, error) { return deleteGuarded(id, nil) }

// DeleteNamed deletes the session id when its name is still name: a
// client binds the delete to the session that the user confirmed, and the
// host refuses it when a rename came in between. An empty name is a name
// too, that of a session with none: a session that got a name since is
// not deleted.
func DeleteNamed(id, name string) (DeleteResult, error) { return deleteGuarded(id, &name) }

// deleteGuarded deletes the session id, when its name is *name, or
// whatever its name is when name is nil.
func deleteGuarded(id string, name *string) (DeleteResult, error) {
	var res DeleteResult
	if err := CheckCaller(); err != nil {
		return res, err
	}
	err := deleteSession(id, name, &res)
	return res, err
}

// DeleteResult says what deleting a session did.
type DeleteResult = api.DeleteResult

func deleteSession(id string, name *string, res *DeleteResult) error {
	return deleteSessionDeclared(id, name, nil, res)
}

func deleteSessionDeclared(id string, name *string, members *[]string, res *DeleteResult) error {
	if !validID(id) {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	if _, err := os.Stat(paths.SessionDir(id)); err != nil {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	primary := id
	if m, err := LoadManifest(id); err == nil && m.SandboxOf != "" {
		var err error
		primary, err = SandboxPrimaryID(id)
		if err != nil {
			return err
		}
	}
	if primary == id {
		if current, err := LoadManifest(id); err == nil && IsNoneSupervisor(current) {
			return deleteNonePrimaryV1(id, name, members, res)
		}
	}
	unlock, err := lockSession(primary)
	if err != nil {
		return err
	}
	defer unlock()
	if primary != id {
		return deleteChildUnderPrimary(primary, id, name, res)
	}
	return deleteLockedGroup(id, name, members, res)
}

func deleteChildUnderPrimary(primary, id string, name *string, res *DeleteResult) error {
	unlock, err := lockSession(id)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := LoadManifest(id)
	if err != nil {
		return err
	}
	if m.ID != id || m.SandboxOf != primary {
		return snapshotChanged()
	}
	return deleteLockedGroup(id, name, nil, res)
}

// deleteLocked is deleteSession for a caller that holds the session's
// lock, such as a start that fails. A nil name deletes whatever the name.
func deleteLocked(id string, name *string, res *DeleteResult) error {
	return deleteLockedGroup(id, name, nil, res)
}

func deleteLockedGroup(id string, name *string, members *[]string, res *DeleteResult) error {
	m, merr := LoadManifest(id)
	if name != nil {
		switch {
		case merr != nil:
			// Fail closed: a name that cannot be read is not the one confirmed.
			return UserError("name_unknown", fmt.Sprintf("the name of session %s cannot be read, so it was not deleted", id),
				"List the sessions, then delete the one you mean.")
		case renameUnsaved(id, m):
			return UserError("session_renamed", fmt.Sprintf("session %s has a new name that its holder has not saved yet, so it was not deleted", id),
				"Wait a moment, then list the sessions and delete the one you mean.")
		case m.Name != *name:
			return UserError("session_renamed", fmt.Sprintf("the session %s is now named %q, not %q, so it was not deleted", id, m.Name, *name),
				"List the sessions, then delete the one you mean.")
		}
	}
	children, err := checkDeleteMembers(m, members)
	if err != nil {
		return err
	}
	afterNameCheck(id)
	var stopErr error
	if m != nil && m.SandboxOf != "" {
		stopErr = stopDeletedMember(m)
	} else {
		stopErr = stopHolder(id, m)
	}
	if err := stopErr; err != nil {
		return UserError("busy", fmt.Sprintf("the holder of session %s could not be stopped, so it was not deleted: %v", id, err),
			"Try again in a moment, or check the host with the host doctor.")
	}
	if err := removeSavedChildren(id, children); err != nil {
		return err
	}
	unlockFirst, err := lockFirstSave(id, renameWait)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !sessionGone(id) {
		return err
	}
	if unlockFirst != nil {
		defer unlockFirst()
	}
	if m != nil {
		var kept *proto.Error
		if res.Worktree, kept = removeWorktree(m.Git); kept != nil {
			res.WorktreeCode, res.WorktreeHint = kept.Code, kept.Hint
		}
	}
	if err := os.RemoveAll(paths.SessionDir(id)); err != nil {
		return fmt.Errorf("remove the files of session %s: %w", id, err)
	}
	res.Deleted = true
	return nil
}

// afterNameCheck runs in a guarded delete after the check of the name,
// under the session's lock. Tests try a rename there.
var afterNameCheck = func(string) {}

// renameWait is how long a rename waits for the session's lock.
var renameWait = 10 * time.Second
