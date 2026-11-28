package host

import (
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

// NewID makes a short random session ID: 8 characters of
// api.SessionIDAlphabet with at least one digit (cli-spec 9.2).
func NewID() string { return api.NewSessionID() }

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
	}
	return taken
}

// checkName refuses a name that a user gives a session, by new or by
// rename, when it looks like a session ID, is longer than
// api.SessionNameMax, or matches the ID of an existing session on this
// host (cli-spec 9.2).
func checkName(name string) error {
	if msg, hint, ok := api.CheckName(name); !ok {
		return UserError("bad_name", msg, hint)
	}
	if name = strings.TrimSpace(name); sessionHasID(name) {
		return UserError("bad_name", "the name "+name+" is the ID of a session on this host",
			"Add a dash, or use another name.")
	}
	return nil
}

// folderName is the name that a new session takes from its folder, cwd,
// when the user gives none. A folder name that looks like an ID or matches
// an existing session's ID gets "-s" at its end.
func folderName(cwd string) string {
	name := filepath.Base(cwd)
	if api.LooksLikeID(name) || sessionHasID(name) {
		name += "-s"
	}
	return name
}

// sessionHasID reports whether a session on this host has the ID id.
func sessionHasID(id string) bool {
	if !validID(id) {
		return false
	}
	_, err := os.Stat(paths.SessionDir(id))
	return err == nil
}

func validID(id string) bool { return api.ValidSessionID(id) }

// holderWentAway reports whether err says that the holder stopped or closed
// its socket during the request.
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

// Rename changes a session's name. It serialises with delete under op.lock
// so a delete confirmed under the old name sees the new name in the
// manifest (cli-spec 9.5).
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
	for range 2 {
		if alive(id) {
			_, err := HolderRequest(id, "rename", map[string]string{"name": name}, renameWait)
			if err == nil || !holderWentAway(err) {
				return err
			}
		}
		unlock, err := lockSessionWait(id, renameWait)
		if err != nil {
			return err
		}
		if alive(id) {
			unlock()
			continue
		}
		defer unlock()
		m, err := LoadManifest(id)
		if err != nil {
			return UserError("not_found", "no session "+id, "List sessions to find its ID.")
		}
		m.Name = name
		return m.Save()
	}
	return UserError("busy", "session "+id+" stopped or started again during the rename, so it was not renamed",
		"List the sessions, then rename it again.")
}
