package host

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

func (owner *purgeOwnerV1) removeOwnedRoots() error {
	if owner.phaseDone("roots") {
		return nil
	}
	if err := owner.replayResource(owner.ctx); err != nil {
		return err
	}
	if err := owner.removeRuntimeCache(); err != nil {
		return err
	}
	if err := owner.removeImages(); err != nil {
		return err
	}
	if err := owner.removeStorageDirectories(); err != nil {
		return err
	}
	var refs map[string]purgeRef
	if err := owner.gate(func(control *placementControl, _ *os.Root) error { refs = control.Refs; return nil }); err != nil {
		return err
	}
	names := make([]string, 0, len(refs))
	for key := range refs {
		if strings.HasPrefix(key, "state/") || strings.HasPrefix(key, "config/") {
			names = append(names, key)
		}
	}
	sort.Strings(names)
	// The original host-id and place.lock remain until every other checked
	// state file has been removed; no subsequent code recreates their domain.
	sort.SliceStable(names, func(i, j int) bool { return purgeStateLastV1(names[i]) < purgeStateLastV1(names[j]) })
	for _, key := range names {
		role, relative, _ := strings.Cut(key, "/")
		validate := func(file *os.File) error {
			if role != "state" {
				return placementError("the configuration resource has no closed native purge validator")
			}
			switch relative {
			case "host-id":
				id, err := readHostIDDescriptor(file)
				if err != nil {
					return err
				}
				if id != owner.args.HostID {
					return placementError("the original host identity changed before removal")
				}
				return nil
			case "place.lock":
				root := owner.roots["state"]
				if root == nil {
					return placementError("the original placement namespace is absent")
				}
				actual, found, err := cachefs.ReadDomainBinding(owner.ctx, root.root, "place.lock", file)
				if err != nil {
					return err
				}
				if !found || actual != owner.placement.domain() {
					return placementError("the placement sentinel changed its native host domain")
				}
				return nil
			}
			metadataRole := stateMetadataRole(relative)
			if !validStateMetadataRole(metadataRole) {
				return placementError("the recorded state resource has an unknown fixed role")
			}
			return validateArchiveDescriptor(owner.ctx, file, func(data []byte) error { return validateStateMetadata(metadataRole, data) })
		}
		if err := owner.removeKnownFile(role, relative, refs[key], validate); err != nil {
			return err
		}
	}
	if ref, ok := refs["directory:state/sessions"]; ok {
		if err := owner.removeEmptyDirectory(paths.SessionsDir(), ref); err != nil {
			owner.reportItem(false, "directory", "state/sessions", err.Error())
		}
	}
	for _, role := range []string{"config", "state"} {
		if err := owner.removeRecordedRoot(role); err != nil {
			return err
		}
	}
	return owner.completePhase("roots")
}

func purgeStateLastV1(name string) int {
	if name == "state/host-id" {
		return 1
	}
	if name == "state/place.lock" {
		return 2
	}
	return 0
}

func (owner *purgeOwnerV1) removeKnownFile(role, relative string, ref purgeRef, validate func(*os.File) error) error {
	var exists bool
	if err := owner.gate(func(control *placementControl, _ *os.Root) error {
		root, err := owner.checkRoot(role, control)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = root.Lstat(relative)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		exists = err == nil
		return err
	}); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := owner.removeFile(role, relative, ref, validate); err != nil {
		if owner.ctx.Err() != nil {
			return errors.Join(owner.ctx.Err(), err)
		}
		owner.reportItem(false, "file", role+"/"+relative, err.Error())
		// A pending capture is retained for exact replay. Continuing would lose
		// its journal or reinterpret the next resource as the original one.
		return err
	}
	owner.reportItem(true, "file", role+"/"+relative, "")
	return nil
}

func (owner *purgeOwnerV1) removeRuntimeCache() error {
	store, err := runtimecache.OpenHostForPurge(owner.ctx, paths.ArtefactsDir(), owner.placement.domain())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		owner.reportItem(false, "runtime", "artefacts/", err.Error())
		return nil
	}
	sentinel, purgeErr := store.PurgeOwned(owner.ctx, runtime.GOARCH, runtimeReferences, func(entry runtimecache.PurgeEntry) error {
		validate := func(file *os.File) error {
			if entry.Item != nil {
				return supply.CheckOpenContext(owner.ctx, *entry.Item, file)
			}
			return nil
		}
		return owner.removeKnownFile("data", filepath.Join("artefacts", entry.Name), refOf(entry.Info), validate)
	})
	closeErr := store.Close()
	if purgeErr != nil || closeErr != nil {
		return errors.Join(purgeErr, closeErr)
	}
	if sentinel != nil {
		// The store nominates its own stable sentinel only after its transaction
		// ends. Remove it only when no preserved blob, record or part remains.
		directory, err := os.Open(paths.ArtefactsDir())
		if err != nil {
			return err
		}
		names, err := directory.Readdirnames(-1)
		_ = directory.Close()
		if err != nil {
			return err
		}
		safe := true
		for _, name := range names {
			if name == ".lock" {
				continue
			}
			child, err := os.ReadDir(filepath.Join(paths.ArtefactsDir(), name))
			if err != nil || len(child) != 0 {
				safe = false
				owner.reportItem(false, "runtime", filepath.Join("artefacts", name), "a preserved or unknown cache resource remains")
			}
		}
		if safe {
			if err := owner.removeKnownFile("data", "artefacts/.lock", refOf(sentinel), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (owner *purgeOwnerV1) removeImages() error {
	store, err := image.OpenHostExisting(owner.ctx, paths.ImagesDir(), owner.placement.domain())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		owner.reportItem(false, "images", "images/", err.Error())
		return nil
	}
	report, purgeErr := store.PurgeOwned(owner.ctx, ImageReferences)
	closeErr := store.Close()
	for _, path := range report.Removed {
		owner.reportItem(true, "image", path, "")
	}
	for _, left := range report.Left {
		owner.reportItem(false, "image", left.Path, left.Reason)
	}
	return errors.Join(purgeErr, closeErr)
}

func (owner *purgeOwnerV1) removeRecordedRoot(role string) error {
	var path string
	var ref purgeRef
	if err := owner.gate(func(control *placementControl, _ *os.Root) error {
		var err error
		path, err = purgeRootPath(control, role)
		ref = control.Roots[role]
		return err
	}); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !ref.validDirectory() {
		owner.reportItem(false, "directory", role, "the directory has no original creation receipt")
		return nil
	}
	if err := owner.removeEmptyDirectory(path, ref); err != nil {
		if owner.ctx.Err() != nil {
			return errors.Join(owner.ctx.Err(), err)
		}
		owner.reportItem(false, "directory", role, err.Error())
		return nil
	}
	owner.reportItem(true, "directory", role, "")
	return nil
}

func (owner *purgeOwnerV1) removeEmptyDirectory(path string, expected purgeRef) error {
	parent, err := cachefs.OpenSafeAncestor(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	leaf := filepath.Base(path)
	capture := ".purge-empty-" + owner.args.Nonce + "-" + leaf
	current, err := parent.Lstat(leaf)
	alreadyCaptured := false
	if errors.Is(err, os.ErrNotExist) {
		current, err = parent.Lstat(capture)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		alreadyCaptured = true
		path = filepath.Join(parent.Name(), capture)
	}
	if err != nil || !expected.matches(current) {
		return placementError("the directory changed its original creation identity")
	}
	root, err := cachefs.OpenSafeAncestor(path)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := file.ReadDir(1)
	_ = file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if len(entries) != 0 {
		return placementError("preserved files remain in the directory")
	}
	if err := errors.Join(owner.ctx.Err(), expected.matchesRoot(root)); err != nil {
		return err
	}
	// Empty-only rmdir cannot remove a replacement containing user files. The
	// exact native generation is captured and checked before the final unlink.
	if !alreadyCaptured {
		if err := cachefs.RenameOwnedDirectoryNoReplace(parent, leaf, capture); err != nil {
			return err
		}
	}
	captured, err := parent.Lstat(capture)
	if err != nil || !expected.matches(captured) {
		return errors.Join(placementError("preserved a changed empty-directory capture"), cachefs.RenameOwnedDirectoryNoReplace(parent, capture, leaf))
	}
	if err := errors.Join(owner.ctx.Err(), expected.matchesRoot(root)); err != nil {
		return err
	}
	return parent.Remove(capture)
}
