package host

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// drawID draws one candidate session ID. Tests replace it to hit a taken
// ID or name first.
var drawID = NewID

// unusedID draws a session ID that no session on this host uses as its ID
// or, from an older helper, as its name (cli-spec 9.2).
func unusedID() string {
	taken := takenIDsAndNames()
	for {
		id := drawID()
		if !taken[id] {
			return id
		}
	}
}

// takenIDsAndNames returns every session ID and session name on this host.
func takenIDsAndNames() map[string]bool {
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return nil
	}
	taken := make(map[string]bool, len(entries)*2)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		taken[id] = true
		if m, err := LoadManifest(id); err == nil && m.Name != "" {
			taken[m.Name] = true
		}
		var u unsavedRename
		if b, err := os.ReadFile(unsavedPath(id)); err == nil && json.Unmarshal(b, &u) == nil {
			if u.Name != "" {
				taken[u.Name] = true
			}
			for _, c := range u.Candidates {
				if c != "" {
					taken[c] = true
				}
			}
		}
	}
	return taken
}

// checkName refuses a name that a user gives a session, by new or by
// rename, when it looks like a session ID, or is longer than
// api.SessionNameMax: an address with an ID's form always means an ID, so
// no name may have that form (cli-spec 9.2). It also refuses the ID of a
// session on this host (nameIsNoID). A session that already has such a
// name keeps it. A name that new takes from the folder never has that
// form (folderName).
func checkName(name string) error {
	if msg, hint, ok := api.CheckName(name); !ok {
		return UserError("bad_name", msg, hint)
	}
	return nameIsNoID(name)
}

// folderName is the name that a new session takes from its folder, cwd,
// when the user gives none. A folder name that looks like an ID gets "-s"
// at its end, so that new never makes a session whose name looks like an
// ID.
func folderName(cwd string) string {
	name := filepath.Base(cwd)
	if api.LooksLikeID(name) {
		name += "-s"
	}
	return name
}

// nameIsNoID refuses name, a name that a user gives a session, when it is
// the ID of a session on this host, as an older helper's ID without a
// digit can be: an address with that word finds the session with that ID
// first, so the name would reach another session (cli-spec 9.2).
func nameIsNoID(name string) error {
	if name = strings.TrimSpace(name); sessionHasID(name) {
		return UserError("bad_name", "the name "+name+" is the ID of a session on this host",
			"Add a dash, or use another name.")
	}
	return nil
}

// sessionHasID reports whether a session on this host has the ID id.
func sessionHasID(id string) bool {
	if !validID(id) {
		return false
	}
	_, err := os.Stat(paths.SessionDir(id))
	return err == nil
}

// freeName is name, the name of the new session id, made free of the IDs
// of the sessions on this host, under the lock of the session's first
// save. A name that the start took from the folder gets "-s" at its end
// when it is another session's ID, as an older ID without a digit can be:
// a name with "-" is no ID. A name that the user gave is refused instead
// (nameIsNoID), as new does before it starts.
func freeName(id, name string, derived bool) (string, error) {
	if name == id || !sessionHasID(name) {
		return name, nil
	}
	if !derived {
		return "", nameIsNoID(name)
	}
	return name + "-s", nil
}

// validID reports whether id safely names a session folder.
func validID(id string) bool { return api.ValidSessionID(id) }

// holderWentAway reports whether err, the error of a request to a
// session's holder, says that the holder went away during the request, as
// when a resume or a delete stops it: the connection closed, or was
// refused, or the socket is gone, without an answer, or the holder
// answered that it was ending. A time-out is not one (holderTimedOut). Any
// other answer is the holder's own.
func holderWentAway(err error) bool {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code == "exiting"
	}
	for _, gone := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.ENOENT, syscall.EPIPE, net.ErrClosed} {
		if errors.Is(err, gone) {
			return true
		}
	}
	return false
}

// holderTimedOut reports whether err says that the holder did not answer
// within the request's time.
func holderTimedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

// renameAnswer is the error of a rename from err, the error of its request
// to the holder: a time-out says that the holder did not answer in time.
func renameAnswer(id string, err error) error {
	if holderTimedOut(err) {
		return UserError("busy", "the holder of session "+id+" did not answer in time, so the session was not renamed",
			"Try again in a moment. If it goes on, check the host with the host doctor.")
	}
	return err
}

// startingCode is the answer of a holder that waits for its secrets: a
// start or a resume holds the session's lock until it has started.
const startingCode = "starting"

// Rename changes a session's name.
//
// The rename runs under the session's lock, as a delete does, so it never
// comes between a delete's check of the name and the delete. The new name
// is in the manifest before Rename answers. A running session's holder
// renames itself, under the lock (holder_rename.go): a rename that its
// caller stops waiting for still runs under the lock. Otherwise Rename
// takes the lock and writes the manifest. A delete reads the name there
// alone. A holder that a resume or a delete stops while its rename waits
// for the lock goes away without an answer: Rename then takes the lock
// itself, and renames the new holder, the stopped session, or finds none.
// The holder of an older helper answers before it saves: Rename then holds
// the lock for it until the manifest has the name (rename_old.go).
func Rename(id, name string) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return UserError("bad_name", "a name is needed", "Pass a non-empty name for the session.")
	}
	if err := checkName(name); err != nil {
		return err
	}
	if !validID(id) {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	for try := range 2 {
		if alive(id) {
			// A holder that went away, or that waits for its secrets while a
			// start holds the lock, gets a second look under the lock.
			err := renameRunning(id, name)
			var pe *proto.Error
			starting := errors.As(err, &pe) && pe.Code == startingCode
			if !holderWentAway(err) && (!starting || try != 0) {
				return renameAnswer(id, err)
			}
		}
		unlock, err := lockSessionWait(id, renameWait)
		if err != nil {
			return err
		}
		unlockFirst, ferr := lockFirstSave(id, renameWait)
		if ferr != nil {
			unlock()
			return ferr
		}
		if alive(id) {
			// A resume or slow restore started it while the rename waited.
			unlockFirst()
			unlock()
			continue
		}
		defer unlock()
		defer unlockFirst()
		m, err := LoadManifest(id)
		if err != nil {
			return UserError("not_found", "no session "+id, "List sessions to find its ID.")
		}
		pendingStart := holderLockHeld(id) && m.Status.State == StateStarting && m.Run.AgentPID == 0 && !socketPresent(id)
		if !pendingStart && oldHolderRuns(id) {
			return UserError("busy", "the holder of session "+id+" did not answer in time, so the session was not renamed",
				"Try again in a moment. If it goes on, check the host with the host doctor.")
		}
		m.Name = name
		if err := m.Save(); err != nil {
			return err
		}
		// No older holder runs, so none can save another name.
		_ = os.Remove(unsavedPath(id))
		return nil
	}
	return UserError("busy", "session "+id+" stopped or started again during the rename, so it was not renamed", "List the sessions, then rename it again.")
}
