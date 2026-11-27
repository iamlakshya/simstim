package host

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// The unit enabler creates its own fixed symlink rather than asking systemctl
// to create an unobservable file. Existing links are never adopted.
func enableFootprintedUnitV1(ctx context.Context, role, unit string) (returned error) {
	if !knownFootprintLeafV1(role, unit) {
		return placementError("the unit enabler has an unknown fixed role")
	}
	owner, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, owner.close()) }()
	if err := ensureFootprintParentV1(owner, role); err != nil {
		return err
	}
	path, err := footprintRolePathV1(role)
	if err != nil {
		return err
	}
	root, err := cachefs.OpenSafeAncestor(path)
	if err != nil {
		return err
	}
	defer root.Close()
	target := "../" + unit
	if info, err := root.Lstat(unit); err == nil {
		got, readErr := root.Readlink(unit)
		if readErr != nil || info.Mode()&os.ModeSymlink == 0 || got != target {
			return placementError("preserved an existing unit enablement with another target")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := footprintNonce()
	if err != nil {
		return err
	}
	prepared := ".footprint-link-" + id
	if err := root.Symlink(target, prepared); err != nil {
		return err
	}
	info, err := root.Lstat(prepared)
	if err != nil {
		return err
	}
	ref := refOf(info)
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	parent, err := dir.Stat()
	_ = dir.Close()
	if err != nil {
		return err
	}
	entry := Footprint{ID: id, Kind: "link", Root: role, Path: unit, Before: "absent", After: target, At: time.Now().UTC(), Status: "intent", Link: &ref, LinkTarget: target, Directory: ptrPurgeRefV1(refOf(parent)), Prepared: prepared}
	if err := withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		control.Footprints = append(control.Footprints, entry)
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	}); err != nil {
		return err
	}
	if err := checkFootprintRootV1(root, role, *entry.Directory); err != nil {
		return err
	}
	if current, err := root.Lstat(prepared); err != nil || !ref.matches(current) {
		return placementError("the original prepared unit link changed")
	}
	if err := cachefs.RenameOwnedSymlinkNoReplace(root, prepared, unit); err != nil {
		return err
	}
	if current, err := root.Lstat(unit); err != nil || !ref.matches(current) {
		return placementError("the unit link changed during publication")
	}
	if err := syncCapturedDirectory(root); err != nil {
		return err
	}
	return withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		entry.Status, entry.Prepared = "complete", ""
		for i := range control.Footprints {
			if control.Footprints[i].ID == id {
				control.Footprints[i] = entry
			}
		}
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	})
}
