package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func checkFootprintRootV1(root *os.Root, role string, expected purgeRef) error {
	path, err := footprintRolePathV1(role)
	if err != nil {
		return err
	}
	current, err := cachefs.OpenSafeAncestor(path)
	if err != nil {
		return err
	}
	defer current.Close()
	if err := expected.matchesRoot(current); err != nil {
		return err
	}
	return expected.matchesRoot(root)
}

func ensureFootprintParentV1(owner *stateMetadataPublisher, role string) error {

	roles, err := baselineParentPaths()
	if err != nil {
		return err
	}
	keys := []string{"config", "systemd", "systemd_user"}
	if role == "slice_control" {
		roles["user_control"] = filepath.Join(roles["systemd"], "user.control")
		roles["slice_dropin"] = filepath.Join(roles["user_control"], brand.Slice+".d")
		keys = []string{"config", "systemd", "user_control", "slice_dropin"}
	} else if role == "default_wants" || role == "timers_wants" {
		keys = append(keys, role)
	} else if bucket, ok := strings.CutPrefix(role, "terminfo:"); ok && validTerminfoBucketV1(bucket) {
		keys = []string{"terminfo", "terminfo_bucket:" + bucket}
		roles[keys[1]] = filepath.Join(roles["terminfo"], bucket)
	} else if role != "systemd_user" {
		return placementError("the outside producer has an unknown parent chain")
	}
	for _, key := range keys {
		path := roles[key]
		root, err := cachefs.OpenSafeAncestor(path)
		if err == nil {
			_ = root.Close()
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := makeFootprintParentV1(owner, key, path); err != nil {
			return err
		}
	}
	return nil
}

func makeFootprintParentV1(owner *stateMetadataPublisher, key, path string) (returned error) {
	parent, err := cachefs.OpenSafeAncestor(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	parentFile, err := parent.Open(".")
	if err != nil {
		return err
	}
	parentInfo, err := parentFile.Stat()
	_ = parentFile.Close()
	if err != nil {
		return err
	}
	id, err := footprintNonce()
	if err != nil {
		return err
	}
	entry := Footprint{ID: id, Kind: "directory", Root: "parent:" + key, Path: filepath.Base(path), Before: "absent", At: time.Now().UTC(), Status: "intent", Directory: ptrPurgeRefV1(refOf(parentInfo)), Prepared: ".footprint-dir-" + id}
	if err := withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		if _, already := control.Parents[key]; already {
			return placementError("the missing outside parent already has a native receipt")
		}
		control.Footprints = append(control.Footprints, entry)
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	}); err != nil {
		return err
	}
	if err := owner.ctx.Err(); err != nil {
		return err
	}
	if err := parent.Mkdir(entry.Prepared, 0o700); err != nil {
		return err
	}
	prepared, err := cachefs.OpenPrivateRoot(filepath.Join(filepath.Dir(path), entry.Prepared), false)
	if err != nil {
		return err
	}
	defer prepared.Close()
	file, err := prepared.Open(".")
	if err != nil {
		return err
	}
	created, err := file.Stat()
	_ = file.Close()
	if err != nil {
		return err
	}
	ref := refOf(created)
	entry.PreparedRef = &ref
	if err := withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		for index := range control.Footprints {
			if control.Footprints[index].ID == id {
				control.Footprints[index] = entry
			}
		}
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	}); err != nil {
		return err
	}
	if err := checkFootprintRootV1(parent, entry.Root, *entry.Directory); err != nil {
		return err
	}
	if err := cachefs.RenameOwnedDirectoryNoReplace(parent, entry.Prepared, entry.Path); err != nil {
		return err
	}
	current, err := parent.Lstat(entry.Path)
	if err != nil || !ref.matches(current) {
		return placementError("preserved an outside parent that changed during publication")
	}
	if err := syncCapturedDirectory(parent); err != nil {
		return err
	}
	return withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		if err := checkFootprintRootV1(parent, entry.Root, *entry.Directory); err != nil {
			return err
		}
		if control.Parents == nil {
			control.Parents = map[string]purgeRef{}
		}
		if _, already := control.Parents[key]; already {
			return placementError("the created outside parent already has another native receipt")
		}
		for _, known := range api.BaselineParentKeys() {
			if key == known {
				control.Parents[key] = ref
			}
		}
		entry.Status, entry.Prepared = "complete", ""
		for index := range control.Footprints {
			if control.Footprints[index].ID == id {
				control.Footprints[index] = entry
			}
		}
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	})
}
