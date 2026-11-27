//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This permit belongs only to the real purge owner and original active
// preparation. It does not accept a stopped subject or create cleanup rights.
type ordinaryActiveGitInspectionPermitV2 struct {
	mu          sync.Mutex
	purge       *purgeOwnerV1
	operation   *SessionOperation
	preparation *OrdinaryPurgePreparationV2
	origin      worktreeOriginV1
	work        *os.Root
	parent      *os.Root
	repository  *os.Root
	common      *os.Root
	mapping     *ordinaryGitMappingV2
	lifetime    context.Context
	cancel      context.CancelFunc
	loans       sync.WaitGroup
	done        chan struct{}
	closeErr    error
	closed      bool
}

func (purge *purgeOwnerV1) beginOrdinaryActiveGitInspectionV2(ctx context.Context, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2) (_ *ordinaryActiveGitInspectionPermitV2, returned error) {
	if purge == nil || ctx == nil || operation == nil || preparation == nil || preparation.owner == nil || preparation.owner.origin != purge.placement {
		return nil, sessionLeaseError("active Git inspection has no original purge, operation and native preparation")
	}
	if err := preparation.Check(ctx, operation); err != nil {
		return nil, err
	}
	manifest, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !ordinaryManifestV1(manifest) || manifest.Git == nil || len(manifest.Git.extra) != 0 {
		return nil, sessionLeaseError("active Git inspection has no supported original ordinary worktree")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	permit := &ordinaryActiveGitInspectionPermitV2{purge: purge, operation: operation, preparation: preparation, lifetime: lifetime, cancel: cancel, done: make(chan struct{})}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, permit.Close())
		}
	}()
	var found bool
	if err := purge.gate(func(control *placementControl, _ *os.Root) error {
		if _, err := purge.checkRoot("data", control); err != nil {
			return err
		}
		permit.origin, found = control.WorktreeOrigins[manifest.ID]
		return nil
	}); err != nil {
		return nil, err
	}
	git, origin := manifest.Git, permit.origin
	work := filepath.Join(WorktreesDir(), origin.Relative)
	if !found || validateWorktreeOriginsV1(map[string]worktreeOriginV1{manifest.ID: origin}) != nil || origin.Phase != "installed" || work != git.Worktree || origin.Repository != git.RepoRoot || origin.Branch != git.Branch || origin.BaseSHA != git.BaseSHA {
		return nil, sessionLeaseError("active Git inspection has no matching installed original worktree creator")
	}
	permit.parent, err = cachefs.OpenPrivateRoot(filepath.Dir(work), false)
	if err != nil {
		return nil, err
	}
	permit.work, err = cachefs.OpenPrivateChild(permit.parent, filepath.Base(work), false)
	if err != nil {
		return nil, err
	}
	permit.repository, err = cachefs.OpenSafeAncestor(origin.Repository)
	if err != nil {
		return nil, err
	}
	permit.common, err = cachefs.OpenSafeAncestor(origin.Common)
	if err != nil {
		return nil, err
	}
	if err := permit.checkRootsV2(); err != nil {
		return nil, err
	}
	permit.mapping, err = captureOrdinaryGitMappingV2(permit.work, permit.common, origin)
	if err != nil {
		return nil, err
	}
	if err := permit.checkCurrentV2(ctx, operation, preparation); err != nil {
		return nil, err
	}
	complete = true
	return permit, nil
}

func (permit *ordinaryActiveGitInspectionPermitV2) checkRootsV2() error {
	if permit == nil || permit.work == nil || permit.parent == nil || permit.repository == nil || permit.common == nil {
		return sessionLeaseError("the active Git inspection lost an original root loan")
	}
	if err := checkWorktreeBirthV2(permit.work, permit.origin.Birth); err != nil {
		return err
	}
	for _, pair := range []struct {
		ref  purgeRef
		root *os.Root
	}{{permit.origin.Directory, permit.work}, {permit.origin.Parent, permit.parent}, {permit.origin.RepoRef, permit.repository}, {permit.origin.CommonRef, permit.common}} {
		if err := pair.ref.matchesRoot(pair.root); err != nil {
			return err
		}
		fresh, err := cachefs.OpenSafeAncestor(pair.root.Name())
		if err != nil {
			return err
		}
		if err := errors.Join(pair.ref.matchesRoot(fresh), fresh.Close()); err != nil {
			return err
		}
	}
	current, err := permit.parent.Lstat(filepath.Base(permit.work.Name()))
	if err != nil || !permit.origin.Directory.matches(current) {
		return errors.Join(sessionLeaseError("the active worktree changed its original parent's named binding"), err)
	}
	return nil
}

func (permit *ordinaryActiveGitInspectionPermitV2) checkCurrentV2(ctx context.Context, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2) error {
	if permit == nil || permit.closed || ctx == nil || operation == nil || preparation == nil || permit.purge == nil || operation != permit.operation || preparation != permit.preparation || preparation.owner == nil || preparation.owner.origin != permit.purge.placement || permit.mapping == nil {
		return sessionLeaseError("the active Git permit lost its same original purge and preparation")
	}
	if err := preparation.Check(ctx, operation); err != nil {
		return err
	}
	if err := permit.purge.gate(func(control *placementControl, _ *os.Root) error {
		if _, err := permit.purge.checkRoot("data", control); err != nil {
			return err
		}
		current, found := control.WorktreeOrigins[operation.Primary()]
		if !found || !reflect.DeepEqual(current, permit.origin) {
			return sessionLeaseError("the active Git permit's original creator receipt changed")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := permit.checkRootsV2(); err != nil {
		return err
	}
	return permit.mapping.Check()
}

// Check requires the same active preparation and original operation, current
// purge drain, installed creator receipt and every original native root.
func (permit *ordinaryActiveGitInspectionPermitV2) Check(ctx context.Context, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2) error {
	if permit == nil {
		return sessionLeaseError("the active Git permit is absent")
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	return permit.checkCurrentV2(ctx, operation, preparation)
}

// These loans are for one actual fixed inspection worker. They are not a
// public constructor, a stopped source or permission to remove any path.
type ordinaryActiveGitRootsV2 struct {
	Work, Admin, Common *os.File
	permit              *ordinaryActiveGitInspectionPermitV2
	once                sync.Once
	closeErr            error
}

func (permit *ordinaryActiveGitInspectionPermitV2) duplicateRootsV2(ctx context.Context, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2) (_ *ordinaryActiveGitRootsV2, returned error) {
	if permit == nil {
		return nil, sessionLeaseError("the active Git permit is absent")
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if err := permit.checkCurrentV2(ctx, operation, preparation); err != nil {
		return nil, err
	}
	roots := &ordinaryActiveGitRootsV2{permit: permit}
	permit.loans.Add(1)
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, roots.Close())
		}
	}()
	for _, pair := range []struct {
		root *os.Root
		file **os.File
	}{{permit.work, &roots.Work}, {permit.mapping.admin, &roots.Admin}, {permit.common, &roots.Common}} {
		file, err := pair.root.Open(".")
		if err != nil {
			return nil, err
		}
		*pair.file = file
	}
	if err := permit.checkCurrentV2(ctx, operation, preparation); err != nil {
		return nil, err
	}
	complete = true
	return roots, nil
}

func (roots *ordinaryActiveGitRootsV2) Close() error {
	if roots == nil {
		return nil
	}
	roots.once.Do(func() {
		for _, file := range []*os.File{roots.Work, roots.Admin, roots.Common} {
			if file != nil {
				roots.closeErr = errors.Join(roots.closeErr, file.Close())
			}
		}
		if roots.permit != nil {
			roots.permit.loans.Done()
		}
	})
	return roots.closeErr
}

func (permit *ordinaryActiveGitInspectionPermitV2) Close() error {
	if permit == nil {
		return nil
	}
	permit.mu.Lock()
	if permit.closed {
		done := permit.done
		permit.mu.Unlock()
		if done != nil {
			<-done
		}
		return permit.closeErr
	}
	permit.closed = true
	if permit.cancel != nil {
		permit.cancel()
	}
	permit.mu.Unlock()
	permit.loans.Wait()
	if permit.mapping != nil {
		permit.closeErr = errors.Join(permit.closeErr, permit.mapping.Close())
	}
	for _, root := range []*os.Root{permit.work, permit.parent, permit.repository, permit.common} {
		if root != nil {
			permit.closeErr = errors.Join(permit.closeErr, root.Close())
		}
	}
	if permit.done != nil {
		close(permit.done)
	}
	return permit.closeErr
}
