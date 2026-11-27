package supervisor

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/api"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

const supervisorRenameWait = 5 * time.Second

var saveManifest = func(target *host.Manifest) error { return target.Save() }

func (s *Supervisor) rename(name string) error {
	if msg, hint, ok := api.CheckName(name); !ok {
		return &proto.Error{Code: "bad_name", Message: msg, Hint: hint}
	}
	return host.WithSessionLease(s.id, supervisorRenameWait, func() error {
		s.saveMu.Lock()
		defer s.saveMu.Unlock()
		if s.closed {
			return &proto.Error{Code: "exiting", Message: "the session is ending", Hint: "List sessions, then rename the current session."}
		}
		var snapshot host.Manifest
		return s.writeManifest(&snapshot, false, &name)
	})
}

// writeLive refreshes a detached snapshot under the single save turn. A tick
// taken before a rename or policy update cannot put old fields back on disk.
func (s *Supervisor) writeLive(target *host.Manifest, last bool) error {
	return s.writeManifest(target, last, nil)
}

func (s *Supervisor) writeManifest(target *host.Manifest, last bool, rename *string) error {
	return s.writeManifestEdit(target, last, rename, nil)
}

func (s *Supervisor) writeManifestEdit(target *host.Manifest, last bool, rename *string, pulled []string) error {
	return s.writeManifestChange(target, last, rename, pulled, nil, nil)
}

// writeManifestChange requires saveMu. Edit is pure on the owned prospective
// snapshot; publish changes only its owned live field after durable success.
// No disk/RPC work runs under mu, and later queued saves refresh live state.
func (s *Supervisor) writeManifestChange(target *host.Manifest, last bool, rename *string, pulled []string, edit func(*host.Manifest) error, publish func(*host.Manifest)) error {
	if s.closed {
		return &proto.Error{Code: "exiting", Message: "the session ended before this metadata commit", Hint: "Resume the session before requesting a new durable change."}
	}
	write := func(disk *host.Manifest) error {
		s.mu.Lock()
		if disk != nil && disk.Name != s.loadedName {
			s.m.Name = disk.Name
		}
		*target = s.snapshot()
		if rename != nil {
			target.Name = *rename
		}
		s.mu.Unlock()
		if pulled != nil {
			if err := host.ApplyPulled(target, pulled); err != nil {
				return err
			}
		}
		if edit != nil {
			if err := edit(target); err != nil {
				return err
			}
		}
		s.mergeVMReferences(target)
		if err := saveManifest(target); err != nil {
			return err
		}
		if rename != nil {
			host.ClearUnsavedRename(s.id)
			s.mu.Lock()
			s.m.Name = *rename
			s.mu.Unlock()
		}
		if pulled != nil {
			s.mu.Lock()
			s.m.Pulled = slices.Clone(target.Pulled)
			s.mu.Unlock()
		}
		if publish != nil {
			s.mu.Lock()
			publish(target)
			s.mu.Unlock()
		}
		return nil
	}
	if !s.firstSaved {
		wrote, err := host.WithFirstManifestLease(s.id, write)
		if !wrote {
			s.closed = true
			return err
		}
		if err != nil {
			return err
		}
		s.firstSaved = true
	} else if err := write(nil); err != nil {
		return err
	}
	if last {
		s.closed = true
	}
	return nil
}

func (s *Supervisor) recordPulled(ctx context.Context, operation *host.SessionOperation, commits []string) error {
	if !operation.Valid(s.id) {
		return pullChanged()
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed {
		return pullChanged()
	}
	var snapshot host.Manifest
	return s.writeManifestEdit(&snapshot, false, nil, commits)
}

func renameFailure(err error) *proto.Error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe
	}
	return &proto.Error{Code: "rename_failed", Message: err.Error(), Hint: "List sessions, then rename the current session."}
}
