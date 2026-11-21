package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// A reservation owns only the newly made directory. Once holder launch
// starts, its files belong to the session and cleanup must leave them.
type sessionReservation struct {
	id       string
	parent   *os.Root
	dir      *os.Root
	created  fs.FileInfo
	launched bool
}

var drawReservedID = api.RandomSessionID

func reserveSessionID(want string) (*sessionReservation, error) {
	if want != "" && !api.ValidSessionID(want) {
		return nil, UserError("bad_id", "the requested session ID is not valid", "Start the session through the client, which chooses its ID.")
	}
	if err := ensureFootprintedSessionParentV1(context.Background()); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(paths.SessionsDir())
	if err != nil {
		return nil, fmt.Errorf("open the sessions folder: %w", err)
	}
	taken := takenIDsAndNames()
	for range 16 {
		id := want
		if id == "" {
			if id, err = drawReservedID(); err != nil {
				_ = parent.Close()
				return nil, UserError("session_id", "could not make a random session ID", "Try the command again.")
			}
		}
		if taken[id] {
			if want == "" {
				continue
			}
			_ = parent.Close()
			return nil, UserError("id_exists", "another session already uses the requested ID as its name", "Start a new session so the client chooses another ID.")
		}
		if err := parent.Mkdir(id, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				if want == "" {
					continue
				}
				_ = parent.Close()
				return nil, UserError("id_exists", "another session already has the requested ID", "Start a new session so the client chooses another ID.")
			}
			_ = parent.Close()
			return nil, fmt.Errorf("reserve the session folder: %w", err)
		}
		created, err := parent.Lstat(id)
		if err != nil {
			_ = parent.Close()
			return nil, fmt.Errorf("check the reserved session folder: %w", err)
		}
		if !created.IsDir() || created.Mode()&os.ModeSymlink != 0 {
			_ = parent.Close()
			return nil, UserError("session_id", "the reserved session folder changed", "Try the command again.")
		}
		dir, err := parent.OpenRoot(id)
		if err != nil {
			_ = parent.Remove(id)
			_ = parent.Close()
			return nil, fmt.Errorf("open the reserved session folder: %w", err)
		}
		return &sessionReservation{id: id, parent: parent, dir: dir, created: created}, nil
	}
	_ = parent.Close()
	return nil, UserError("session_id", "could not reserve a new session ID", "Try the command again.")
}

func (r *sessionReservation) close() {
	defer func() { _ = r.parent.Close() }()
	defer func() { _ = r.dir.Close() }()
	if r.launched {
		return
	}
	// Work through the pinned directory, rather than a path that another
	// process could have moved or replaced while the start was checked.
	if f, err := r.dir.Open("."); err == nil {
		entries, _ := f.ReadDir(-1)
		_ = f.Close()
		for _, entry := range entries {
			_ = r.dir.RemoveAll(entry.Name())
		}
	}
	if current, err := r.parent.Lstat(r.id); err == nil && os.SameFile(r.created, current) {
		_ = r.parent.Remove(r.id)
	}
}
