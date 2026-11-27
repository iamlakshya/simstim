package host

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (owner *purgeOwnerV1) phaseDone(phase string) bool {
	for _, done := range owner.report.Done {
		if done == phase {
			return true
		}
	}
	return false
}

func (owner *purgeOwnerV1) reportItem(removed bool, kind, path, reason string) {
	item := api.PurgeItem{Kind: kind, Path: path, Reason: reason}
	if !removed {
		item.Hint = "Keep this resource and inspect its original ownership before removing it by hand."
	}
	rows := &owner.report.Left
	if removed {
		rows = &owner.report.Removed
	}
	for _, old := range *rows {
		if old.Kind == kind && old.Path == path && old.Reason == reason {
			return
		}
	}
	*rows = append(*rows, item)
}

func (owner *purgeOwnerV1) undoFootprints() error {
	if owner.phaseDone("footprints") {
		return nil
	}
	var entries []Footprint
	if err := owner.gate(func(control *placementControl, _ *os.Root) error {
		entries = append(entries, control.Footprints...)
		return nil
	}); err != nil {
		return err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Status == "removed" || entry.Status == "left" {
			continue
		}
		if err := owner.ctx.Err(); err != nil {
			return err
		}
		err := owner.undoFootprint(&entry)
		if owner.ctx.Err() != nil {
			return errors.Join(owner.ctx.Err(), err)
		}
		if err != nil {
			entry.Status = "left"
			owner.reportItem(false, entry.Kind, footprintDisplayPathV1(entry.Root, entry.Path), err.Error())
		} else {
			entry.Status = "removed"
			owner.reportItem(true, entry.Kind, footprintDisplayPathV1(entry.Root, entry.Path), "")
		}
		if err := owner.gate(func(control *placementControl, _ *os.Root) error { control.Footprints[i] = entry; return nil }); err != nil {
			return err
		}
	}
	if SystemdUser() {
		if _, err := systemctl(owner.ctx, "daemon-reload"); err != nil {
			return err
		}
	}
	return owner.completePhase("footprints")
}

func (owner *purgeOwnerV1) saveFootprint(entry Footprint) error {
	return owner.gate(func(control *placementControl, _ *os.Root) error {
		for i := range control.Footprints {
			if control.Footprints[i].ID == entry.ID {
				control.Footprints[i] = entry
				return nil
			}
		}
		return placementError("the original footprint disappeared from its journal")
	})
}

func (owner *purgeOwnerV1) undoFootprint(entry *Footprint) error {
	if len(entry.extra) != 0 || !ValidHostID(entry.ID) || !knownFootprintLeafV1(entry.Root, entry.Path) || entry.Directory == nil {
		return placementError("the footprint has no complete native producer receipt")
	}
	path, err := footprintRolePathV1(entry.Root)
	if err != nil {
		return err
	}
	root, err := cachefs.OpenSafeAncestor(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkFootprintRootV1(root, entry.Root, *entry.Directory); err != nil {
		return err
	}
	switch entry.Kind {
	case "unit", "terminfo", "slice":
		if entry.Owned == nil {
			return placementError("the outside file has no original prepared inode")
		}
		if entry.Kind == "unit" && entry.Status == "complete" {
			// Only an owned unit is stopped. Enablement links are separate original
			// creations; systemctl disable/revert would remove unknown other files.
			file, got, err := openFootprintFileV1(root, entry.Path)
			if err == nil {
				_ = file.Close()
				if !footprintRefEqualV1(got, entry.Owned) {
					return placementError("the unit was changed after installation")
				}
				if _, err := systemctl(owner.ctx, "stop", entry.Path); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := owner.removeFootprintFile(root, entry, entry.Path, entry.Owned); err != nil {
			return err
		}
		if entry.Prepared != "" {
			if !strings.HasPrefix(entry.Prepared, ".footprint-part-") || !ValidHostID(strings.TrimPrefix(entry.Prepared, ".footprint-part-")) {
				return placementError("the prepared footprint name is unknown")
			}
			if err := owner.removeFootprintFile(root, entry, entry.Prepared, entry.Owned); err != nil {
				return err
			}
		}
		if entry.Original != nil {
			if entry.Previous == "" || entry.Backup != ".footprint-old-"+entry.ID {
				return placementError("the prior outside file lacks its exact original update receipt")
			}
			file, got, err := openFootprintFileV1(root, entry.Backup)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			defer file.Close()
			if !footprintRefEqualV1(got, entry.Original) {
				return placementError("the retained prior file changed")
			}
			if err := cachefs.RenameOwnedFileNoReplace(root, file, entry.Backup, entry.Path, 0o644); err != nil {
				return err
			}
			restored, got, err := openFootprintFileV1(root, entry.Path)
			if err != nil {
				return err
			}
			_ = restored.Close()
			if !footprintRefEqualV1(got, entry.Original) {
				return placementError("the prior outside file changed during restoration")
			}
		}
	case "link":
		if entry.Link == nil || entry.LinkTarget != "../"+entry.Path {
			return placementError("the unit link lacks its original creation receipt")
		}
		if err := owner.removeFootprintLink(root, entry, entry.Path); err != nil {
			return err
		}
		if entry.Prepared != "" {
			if entry.Prepared != ".footprint-link-"+entry.ID {
				return placementError("the prepared unit link has an unknown name")
			}
			if err := owner.removeFootprintLink(root, entry, entry.Prepared); err != nil {
				return err
			}
		}
	case "directory":
		if entry.PreparedRef == nil || !entry.PreparedRef.validDirectory() {
			return placementError("the outside directory has no original native creation receipt")
		}
		if err := owner.removeFootprintDirectory(root, entry, entry.Path); err != nil {
			return err
		}
		if entry.Prepared != "" {
			if entry.Prepared != ".footprint-dir-"+entry.ID {
				return placementError("the prepared outside directory has an unknown name")
			}
			if err := owner.removeFootprintDirectory(root, entry, entry.Prepared); err != nil {
				return err
			}
		}
	default:
		return placementError("this setting has no native reversible producer receipt")
	}
	return syncCapturedDirectory(root)
}

func (owner *purgeOwnerV1) removeFootprintFile(root *os.Root, entry *Footprint, leaf string, expected *footprintRef) error {
	capture := ".footprint-purge-" + entry.ID
	if entry.Quarantine != "" && entry.Quarantine != capture {
		return placementError("the outside capture has an unknown journal name")
	}
	file, got, err := openFootprintFileV1(root, leaf)
	if errors.Is(err, os.ErrNotExist) {
		file, got, err = openFootprintFileV1(root, capture)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
	} else if err == nil {
		if !footprintRefEqualV1(got, expected) {
			_ = file.Close()
			return placementError("the outside file changed from its producer receipt")
		}
		entry.Quarantine = capture
		if err := owner.saveFootprint(*entry); err != nil {
			_ = file.Close()
			return err
		}
		err = cachefs.RenameOwnedFileNoReplace(root, file, leaf, capture, 0o644)
		_ = file.Close()
		if err != nil {
			return err
		}
		file, got, err = openFootprintFileV1(root, capture)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if !footprintRefEqualV1(got, expected) {
		return errors.Join(placementError("preserved a changed outside file capture"), cachefs.RenameOwnedFileNoReplace(root, file, capture, leaf, 0o644))
	}
	if err := errors.Join(owner.ctx.Err(), checkFootprintRootV1(root, entry.Root, *entry.Directory)); err != nil {
		return err
	}
	now, err := footprintFileRefV1(file)
	if err != nil || !footprintRefEqualV1(now, expected) {
		return placementError("the captured outside file changed before unlink")
	}
	if err := root.Remove(capture); err != nil {
		return err
	}
	entry.Quarantine = ""
	return syncCapturedDirectory(root)
}

func (owner *purgeOwnerV1) removeFootprintLink(root *os.Root, entry *Footprint, leaf string) error {
	capture := ".footprint-unlink-" + entry.ID
	current, err := root.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		leaf = capture
		current, err = root.Lstat(leaf)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	target, err := root.Readlink(leaf)
	if err != nil || !entry.Link.matches(current) || target != entry.LinkTarget {
		return placementError("the unit link changed from its original native receipt")
	}
	if leaf != capture {
		entry.Quarantine = capture
		if err := owner.saveFootprint(*entry); err != nil {
			return err
		}
		if err := cachefs.RenameOwnedSymlinkNoReplace(root, leaf, capture); err != nil {
			return err
		}
	}
	current, err = root.Lstat(capture)
	target, readErr := root.Readlink(capture)
	if err != nil || readErr != nil || !entry.Link.matches(current) || target != entry.LinkTarget {
		return placementError("preserved a changed captured unit link")
	}
	if err := errors.Join(owner.ctx.Err(), checkFootprintRootV1(root, entry.Root, *entry.Directory)); err != nil {
		return err
	}
	if err := root.Remove(capture); err != nil {
		return err
	}
	entry.Quarantine = ""
	return syncCapturedDirectory(root)
}

func (owner *purgeOwnerV1) removeFootprintDirectory(parent *os.Root, entry *Footprint, leaf string) error {
	capture := ".footprint-rmdir-" + entry.ID
	info, err := parent.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		leaf = capture
		info, err = parent.Lstat(leaf)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	if !entry.PreparedRef.matches(info) {
		return placementError("the outside directory changed its native generation")
	}
	child, err := cachefs.OpenPrivateRoot(filepath.Join(parent.Name(), leaf), false)
	if err != nil {
		return err
	}
	defer child.Close()
	file, err := child.Open(".")
	if err != nil {
		return err
	}
	names, err := file.ReadDir(1)
	_ = file.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) != 0 {
		return placementError("the outside directory still contains preserved files")
	}
	if leaf != capture {
		entry.Quarantine = capture
		if err := owner.saveFootprint(*entry); err != nil {
			return err
		}
		if err := cachefs.RenameOwnedDirectoryNoReplace(parent, leaf, capture); err != nil {
			return err
		}
	}
	if info, err := parent.Lstat(capture); err != nil || !entry.PreparedRef.matches(info) {
		return placementError("preserved a changed captured outside directory")
	}
	if err := errors.Join(owner.ctx.Err(), entry.PreparedRef.matchesRoot(child), checkFootprintRootV1(parent, entry.Root, *entry.Directory)); err != nil {
		return err
	}
	// rmdir is empty-only; a new child never turns into recursive deletion.
	return parent.Remove(capture)
}
