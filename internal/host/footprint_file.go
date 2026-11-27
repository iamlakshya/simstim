package host

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

func footprintFileRefV1(file *os.File) (*footprintRef, error) {
	info, err := cachefs.ValidateDescriptor(file, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(proto.MaxFrame)+1))
	if err != nil || len(data) > proto.MaxFrame {
		return nil, placementError("the outside file is unreadable or exceeds its metadata bound")
	}
	after, err := cachefs.ValidateDescriptor(file, 0o644)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, placementError("the outside file changed while its contents were inspected")
	}
	d, i, u, _, ok := footprintIdentity(info)
	if !ok {
		return nil, placementError("the outside file has no native identity")
	}
	return &footprintRef{Device: d, Inode: i, UID: u, Mode: uint32(info.Mode()), Size: info.Size(), ModTime: info.ModTime(), SHA256: footprintDigest(data)}, nil
}

func footprintRefMatchesV1(ref *footprintRef, info os.FileInfo) bool {
	if ref == nil || len(ref.extra) != 0 {
		return false
	}
	return (purgeRef{Device: ref.Device, Inode: ref.Inode, UID: ref.UID, Mode: ref.Mode, Size: ref.Size, ModTime: ref.ModTime}).matches(info)
}

func openFootprintFileV1(root *os.Root, leaf string) (*os.File, *footprintRef, error) {
	info, err := root.Lstat(leaf)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 || !wallCacheOwned(info) {
		return nil, nil, placementError("preserved an outside file with unknown ownership or mode")
	}
	file, err := root.Open(leaf)
	if err != nil {
		return nil, nil, err
	}
	ref, err := footprintFileRefV1(file)
	if err != nil || !footprintRefMatchesV1(ref, info) {
		_ = file.Close()
		return nil, nil, placementError("the outside file changed while its descriptor was opened")
	}
	return file, ref, nil
}

// writeFootprintedUnitV1 is the real fixed-role file producer used by setup.
// Unknown existing units are preserved. The ahead guard names the exact
// prepared inode before the public unit name can change.
func writeFootprintedUnitV1(ctx context.Context, leaf, text string) error {
	return writeFootprintedFileV1(ctx, "systemd_user", leaf, []byte(text))
}

func writeFootprintedFileV1(ctx context.Context, role, leaf string, contents []byte) (returned error) {
	if !knownFootprintLeafV1(role, leaf) {
		return placementError("the unit writer has no fixed unit role")
	}
	owner, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, owner.close()) }()
	path, err := footprintRolePathV1(role)
	if err != nil {
		return err
	}
	if err := ensureFootprintParentV1(owner, role); err != nil {
		return err
	}
	root, err := cachefs.OpenSafeAncestor(path)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	parent, err := dir.Stat()
	_ = dir.Close()
	if err != nil {
		return err
	}
	if err := withFootprintControlV1(owner, func(control *placementControl, _ *os.Root) error {
		for _, prior := range control.Footprints {
			if prior.Root == role && prior.Path == leaf && prior.Status == "intent" {
				return UserError("footprint", "an earlier unit publication is unfinished", "Keep the host state and inspect the recorded prepared unit before retrying setup.")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	var old *footprintRef
	file, before, err := openFootprintFileV1(root, leaf)
	if err == nil {
		_ = file.Close()
		if before.SHA256 == footprintDigest(contents) {
			return withFootprintControlV1(owner, func(control *placementControl, _ *os.Root) error {
				for _, prior := range control.Footprints {
					if prior.Root == role && prior.Path == leaf && prior.Status == "complete" && footprintRefEqualV1(prior.Owned, before) {
						return nil
					}
				}
				return UserError("footprint", "preserved an existing matching file without its original producer receipt", "Keep the file for inspection before explicitly moving it aside and retrying setup.")
			})
		}
		old = before
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := footprintNonce()
	if err != nil {
		return err
	}
	temporary := ".footprint-part-" + id
	//nolint:gosec // G302: public systemd unit text, matching the installed unit.
	prepared, err := root.OpenFile(temporary, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer prepared.Close()
	defer func() {
		if current, err := root.Lstat(temporary); err == nil {
			if original, statErr := cachefs.ValidateDescriptor(prepared, 0o644); statErr == nil && os.SameFile(current, original) {
				_ = root.Remove(temporary)
			}
		}
	}()
	if _, err := prepared.Write(contents); err != nil {
		return err
	}
	if err := prepared.Sync(); err != nil {
		return err
	}
	owned, err := footprintFileRefV1(prepared)
	if err != nil {
		return err
	}
	entry := Footprint{ID: id, Kind: footprintFileKindV1(role), Root: role, Path: leaf, Before: "absent", After: owned.SHA256, At: time.Now().UTC(), Status: "intent", Owned: owned, Prepared: temporary, Directory: ptrPurgeRefV1(refOf(parent))}
	if old != nil {
		entry.Before, entry.Original = "owned", old
		entry.Backup = ".footprint-old-" + id
	}
	if err := withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		if old != nil {
			for index := len(control.Footprints) - 1; index >= 0; index-- {
				prior := control.Footprints[index]
				if prior.Root == entry.Root && prior.Path == entry.Path && prior.Status == "complete" && footprintRefEqualV1(prior.Owned, old) {
					entry.Previous = prior.ID
					break
				}
			}
			if entry.Previous == "" {
				return UserError("footprint", "preserved an existing unit without its original producer receipt", "Keep the unit for inspection before explicitly moving it aside and retrying setup.")
			}
		}
		control.Footprints = append(control.Footprints, entry)
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	}); err != nil {
		return err
	}
	if old != nil {
		if err := checkFootprintRootV1(root, role, refOf(parent)); err != nil {
			return err
		}
		oldFile, now, err := openFootprintFileV1(root, leaf)
		if err != nil {
			return err
		}
		defer oldFile.Close()
		if !footprintRefEqualV1(now, old) {
			return placementError("the old unit changed before its native capture")
		}
		capture := entry.Backup
		if err := cachefs.RenameOwnedFileNoReplace(root, oldFile, leaf, capture, 0o644); err != nil {
			return err
		}
		captured, got, err := openFootprintFileV1(root, capture)
		if err != nil {
			return err
		}
		_ = captured.Close()
		if !footprintRefEqualV1(got, old) {
			return placementError("preserved an unexpected captured unit generation")
		}
		// The old generation is retained until the new publication is complete.
	}
	if err := checkFootprintRootV1(root, role, refOf(parent)); err != nil {
		return err
	}
	if err := cachefs.RenameOwnedFileNoReplace(root, prepared, temporary, leaf, 0o644); err != nil {
		return err
	}
	if err := syncCapturedDirectory(root); err != nil {
		return err
	}
	return withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		if err := checkFootprintRootV1(root, role, refOf(parent)); err != nil {
			return err
		}
		for index := range control.Footprints {
			if control.Footprints[index].ID == id {
				entry.Status, entry.Prepared = "complete", ""
				control.Footprints[index] = entry
			}
		}
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	})
}

func ptrPurgeRefV1(ref purgeRef) *purgeRef { return &ref }
func footprintRefEqualV1(a, b *footprintRef) bool {
	return a != nil && b != nil && len(a.extra) == 0 && len(b.extra) == 0 && a.Device == b.Device && a.Inode == b.Inode && a.UID == b.UID && a.Mode == b.Mode && a.Size == b.Size && a.ModTime.Equal(b.ModTime) && a.SHA256 == b.SHA256
}

func footprintFileKindV1(role string) string {
	if role == "systemd_user" {
		return "unit"
	}
	if role == "slice_control" {
		return "slice"
	}
	return "terminfo"
}
