package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// saveHostsWithOPAdmission is the bulk entry only. The canonical owned
// UpdateHosts mutator remains separate and uses the SAME final writer.
func saveHostsWithOPAdmission(ctx context.Context, hosts []Host) error {
	if ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostStateInspectionKey{}) != nil {
		return hostStateRemovalError("membership cannot change during verified cleanup or owner inspection")
	}
	frozen := make([]Host, len(hosts))
	for i, h := range hosts {
		if err := ctx.Err(); err != nil {
			return err
		}
		frozen[i] = cloneObservationHost(h)
	}
	locked, release, err := acquireHostMembershipWriteContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	old, err := LoadHosts()
	if err != nil {
		return err
	}
	changed, err := changedBulkHostNames(locked, old, frozen)
	if err != nil {
		return err
	}
	for _, name := range changed {
		if err := locked.Err(); err != nil {
			return err
		}
		if !ValidHostName(name) {
			// Legacy invalid entries have no safe OP path. This preserves the
			// existing metadata-repair API without creating a forged domain.
			continue
		}
		if err := probeBulkHostOperation(locked, name); err != nil {
			return err
		}
	}
	beforeHostsSave()
	if err := locked.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	return writeHostsFile(frozen)
}

// All rows of a repeated legacy name participate. A duplicate group is
// conservatively probed even if its input order is unchanged, because it has
// no single unambiguous target entry. No last-value map hides an active row.
func changedBulkHostNames(ctx context.Context, old, next []Host) ([]string, error) {
	before, after := map[string][]Host{}, map[string][]Host{}
	for _, h := range old {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before[h.Name] = append(before[h.Name], h)
	}
	for _, h := range next {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		after[h.Name] = append(after[h.Name], h)
	}
	names := map[string]bool{}
	for name, rows := range before {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		other := after[name]
		if len(rows) > 1 || !slices.EqualFunc(rows, other, SameEntry) {
			names[name] = true
		}
	}
	for name, rows := range after {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		other := before[name]
		if len(rows) > 1 || !slices.EqualFunc(rows, other, SameEntry) {
			names[name] = true
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, ctx.Err()
}

// probeBulkHostOperation holds only ONE transient OP FD. It creates nothing
// and releases its native lock before the next name. Membership remains held
// through all probes and commit, so delayed Phase B must wait at Phase C.
func probeBulkHostOperation(ctx context.Context, name string) (failure error) {
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	path := hostLockPath(name)
	parent := filepath.Dir(path)
	root, err := cachefs.OpenPrivateRoot(parent, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return wrapHostOperationError(name, path, err)
	}
	defer func() {
		if err := root.Close(); failure == nil && err != nil {
			failure = wrapHostOperationError(name, path, err)
		}
	}()
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return wrapHostOperationError(name, path, err)
	}
	leaf := filepath.Base(path)
	file, err := cachefs.OpenFile(root, leaf, os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return wrapHostOperationError(name, path, err)
	}
	locked := false
	defer func() {
		var unlockErr error
		if locked {
			unlockErr = unlockFile(file)
		}
		closeErr := file.Close()
		if failure == nil {
			if unlockErr != nil {
				failure = wrapHostOperationError(name, path, unlockErr)
			} else if closeErr != nil {
				failure = wrapHostOperationError(name, path, closeErr)
			}
		}
	}()
	ref, err := cachefs.CaptureFile(root, leaf, file, 0600)
	if err != nil {
		return wrapHostOperationError(name, path, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := takeLock(file); err != nil {
		if errors.Is(err, errLockTaken) {
			return &UserError{Msg: fmt.Sprintf("%s is busy, so the host list was not saved", name),
				Hint: "Wait until that host operation ends, then try again.", Code: "host_busy"}
		}
		return flockFailed("the host "+name, path, err)
	}
	locked = true
	owned := &hostOperationFile{root: root, rootPath: parent, stateDir: StateDir(), name: leaf, parent: rootRef, file: file, reference: ref}
	if err := owned.normalHeader(ctx); err != nil {
		return wrapHostOperationError(name, path, err)
	}
	return ctx.Err()
}
