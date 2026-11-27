//go:build linux || darwin

package none

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/fsbirth"
	"github.com/iamlakshya/simstim/internal/paths"
)

func (s *sandbox) prepareRuntime(ctx context.Context) error {
	base, err := paths.OpenPrivateRuntimeDir()
	if err != nil {
		return err
	}
	defer base.Close()
	if err := cachefs.MakePrivateDir(base, "g"); err != nil {
		return err
	}
	parent, err := cachefs.OpenPrivateChild(base, "g", false)
	if err != nil {
		return err
	}
	s.runtimeParent = parent
	s.runtimeParentRef, err = cachefs.CaptureRoot(parent)
	if err != nil {
		return err
	}
	s.runtimeParentBirth, err = fsbirth.Capture(parent)
	if err != nil {
		return err
	}
	path := filepath.Join(parent.Name(), s.spec.ID)
	if len(filepath.Join(path, "holder-"+s.spec.ID+".sock")) > paths.MaxUnixSocketPath {
		return failure("the unwalled holder's runtime socket path is too long", nil)
	}
	if err := parent.Mkdir(s.spec.ID, 0700); err != nil {
		return failure("an earlier unwalled runtime still owns its private folder", err)
	}
	s.created, err = parent.Lstat(s.spec.ID)
	if err != nil {
		return err
	}
	s.runtime, err = cachefs.OpenPrivateChild(parent, s.spec.ID, false)
	if err != nil {
		return err
	}
	s.runtimeRef, err = cachefs.CaptureRoot(s.runtime)
	if err != nil {
		return err
	}
	s.runtimeBirth, err = fsbirth.Capture(s.runtime)
	return errors.Join(err, ctx.Err())
}

// DirectoryCreationDataV1 is DATA from this actual creator's mkdir/open. It
// returns no path, descriptor, ownership, cleanup or execution permission.
type DirectoryCreationDataV1 struct {
	Parent, Directory           cachefs.Reference
	ParentBirth, DirectoryBirth *fsbirth.DataV2
}

func cloneDirectoryBirthV1(data *fsbirth.DataV2) *fsbirth.DataV2 {
	if data == nil {
		return nil
	}
	copy := *data
	copy.Handle = append([]byte(nil), data.Handle...)
	return &copy
}

func (owner *LiveOwnerV1) DirectoryCreationDataV1(ctx context.Context) (DirectoryCreationDataV1, error) {
	empty := DirectoryCreationDataV1{}
	if err := owner.Check(ctx); err != nil {
		return empty, err
	}
	s := owner.runtime
	for !s.life.TryLock() {
		select {
		case <-ctx.Done():
			return empty, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.life.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != owner || s.closed || !s.started || !s.handed || s.cmd == nil || s.connection != owner.connection || s.runtime == nil || s.runtimeParent == nil {
		return empty, failure("the original unwalled directory creator ended or changed", nil)
	}
	if err := errors.Join(s.rootRef.CheckRoot(s.root), s.runtimeParentRef.CheckRoot(s.runtimeParent), s.runtimeRef.CheckRoot(s.runtime), checkUnwalledHandle(s.pidfd), ctx.Err()); err != nil {
		return empty, err
	}
	return DirectoryCreationDataV1{Parent: s.runtimeParentRef, Directory: s.runtimeRef, ParentBirth: cloneDirectoryBirthV1(s.runtimeParentBirth), DirectoryBirth: cloneDirectoryBirthV1(s.runtimeBirth)}, nil
}
