package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

func recordStorageDirV1(control *placementControl, guard *os.Root, path string, info os.FileInfo) error {
	ref := refOf(info)
	if !ref.validDirectory() {
		return placementError("the new storage directory has no native creation identity")
	}
	clean := filepath.Clean(path)
	if clean == filepath.Clean(paths.DataDir()) {
		if control.Roots == nil {
			control.Roots = map[string]purgeRef{}
		}
		control.Roots["data"] = ref
		return writePlacementControl(guard, *control)
	}
	if relative, err := filepath.Rel(paths.DataDir(), clean); err == nil && filepath.IsLocal(relative) && relative != "." {
		if control.Refs == nil {
			control.Refs = map[string]purgeRef{}
		}
		control.Refs["directory:data/"+relative] = ref
		return writePlacementControl(guard, *control)
	}
	parents, err := baselineParentPaths()
	if err != nil {
		return err
	}
	matched := false
	for key, target := range parents {
		if clean == filepath.Clean(target) {
			if control.Parents == nil {
				control.Parents = map[string]purgeRef{}
			}
			control.Parents[key] = ref
			matched = true
		}
	}
	if matched {
		return writePlacementControl(guard, *control)
	}
	return placementError("the native storage producer created an unknown parent role")
}

// prepareStorageFootprintsV1 is an actual native directory producer under the
// existing publisher. Only successful original mkdir callbacks add receipts.
// Existing roots and same-name directories are not adopted by this helper.
func (p *runtimePublication) prepareStorageFootprintsV1(ctx context.Context, family string) error {
	children := []string{}
	root := ""
	switch family {
	case "runtime":
		root = paths.ArtefactsDir()
		children = []string{"sha256", "incoming", "runtime"}
	case "images":
		root = paths.ImagesDir()
		children = []string{"incoming", "ownership"}
	default:
		return placementError("the storage producer has an unknown fixed family")
	}
	p.activity.Lock()
	defer p.activity.Unlock()
	if err := p.checkCaller(); err != nil {
		return err
	}
	return withPlacementRoot(ctx, false, func(control *placementControl, guard *os.Root) error {
		if p.ctx.Err() != nil {
			return context.Cause(p.ctx)
		}
		if current, ok := control.Active[p.operation.token]; !ok || current != p.operation.record {
			return placementError("the storage directory producer lost its original publisher")
		}
		record := func(path string, info os.FileInfo) error {
			return recordStorageDirV1(control, guard, path, info)
		}
		targets := []string{root}
		for _, child := range children {
			targets = append(targets, filepath.Join(root, child))
		}
		for _, path := range targets {
			dir, err := cachefs.OpenPrivateRootCreated(path, record)
			if err != nil {
				return err
			}
			if err := dir.Close(); err != nil {
				return err
			}
		}
		return nil
	}, true)
}

func (owner *purgeOwnerV1) removeStorageDirectories() error {
	var entries map[string]purgeRef
	if err := owner.gate(func(control *placementControl, _ *os.Root) error { entries = control.Refs; return nil }); err != nil {
		return err
	}
	// Longest paths first: every unlink remains an exact native empty-only rmdir.
	names := []string{}
	for key := range entries {
		if strings.HasPrefix(key, "directory:data/") {
			names = append(names, strings.TrimPrefix(key, "directory:data/"))
		}
	}
	sortLongestPurgePathsV1(names)
	for _, name := range names {
		if name == "bin" || strings.HasPrefix(name, "bin/") {
			continue
		}
		if err := owner.removeEmptyDirectory(filepath.Join(paths.DataDir(), name), entries["directory:data/"+name]); err != nil {
			if owner.ctx.Err() != nil {
				return err
			}
			owner.reportItem(false, "directory", name, err.Error())
		} else {
			owner.reportItem(true, "directory", name, "")
		}
	}
	return nil
}

func (owner *purgeOwnerV1) removeCreatedParents() error {
	var receipts map[string]purgeRef
	if err := owner.gate(func(control *placementControl, _ *os.Root) error { receipts = control.Parents; return nil }); err != nil {
		return err
	}
	roles, err := baselineParentPaths()
	if err != nil {
		return err
	}
	names := []string{}
	byPath := map[string]purgeRef{}
	for role, ref := range receipts {
		if path, known := roles[role]; known {
			names = append(names, path)
			byPath[path] = ref
		}
	}
	sortLongestPurgePathsV1(names)
	for _, path := range names {
		if err := owner.removeEmptyDirectory(path, byPath[path]); err != nil {
			if owner.ctx.Err() != nil {
				return err
			}
			owner.reportItem(false, "parent-directory", path, err.Error())
		}
	}
	return nil
}

func sortLongestPurgePathsV1(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) > len(paths[j])
		}
		return paths[i] < paths[j]
	})
}

func ensureFootprintedSessionParentV1(ctx context.Context) (returned error) {
	owner, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, owner.close()) }()
	return withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		root, err := cachefs.OpenPrivateRootCreated(paths.SessionsDir(), func(path string, info os.FileInfo) error {
			if path != paths.SessionsDir() || !refOf(info).validDirectory() {
				return placementError("the session directory producer created an unknown parent")
			}
			if control.Refs == nil {
				control.Refs = map[string]purgeRef{}
			}
			control.Refs["directory:state/sessions"] = refOf(info)
			return writePlacementControl(guard, *control)
		})
		if err != nil {
			return err
		}
		return root.Close()
	})
}

func (owner *purgeOwnerV1) removeRecordedRuntimeRootsV1() error {
	var receipts map[string]runtimeRootReceipt
	if err := owner.gate(func(control *placementControl, _ *os.Root) error { receipts = control.RuntimeRoots; return nil }); err != nil {
		return err
	}
	names := make([]string, 0, len(receipts))
	for key := range receipts {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		receipt := receipts[key]
		if _, err := os.Lstat(receipt.Path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if !receipt.Created || !receipt.Ref.validDirectory() {
			owner.reportItem(false, "runtime-directory", receipt.Path, "the runtime directory has no original native creation receipt")
			continue
		}
		if err := owner.removeEmptyDirectory(receipt.Path, receipt.Ref); err != nil {
			if owner.ctx.Err() != nil {
				return err
			}
			owner.reportItem(false, "runtime-directory", receipt.Path, err.Error())
		} else {
			owner.reportItem(true, "runtime-directory", receipt.Path, "")
		}
	}
	return nil
}
