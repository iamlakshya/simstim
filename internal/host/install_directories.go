package host

import (
	"context"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// This observer is called only from the existing native mkdir factory while
// the closed installer is running. A loaded reference or baseline claim never
// enters it and cannot mark a pre-existing directory as created by this tool.
func (p *placement) observeInstallerDirectoryV1(path string, info os.FileInfo) error {
	if !p.installer || !refOf(info).validDirectory() || path != filepath.Clean(path) {
		return placementError("the installation directory has no original creation receipt")
	}
	if p.installerCreated == nil {
		p.installerCreated = map[string]purgeRef{}
	}
	p.installerCreated[path] = refOf(info)
	return nil
}

func (p *placement) commitInstallerDirectoriesV1(control *placementControl) error {
	if !p.installer || control.HostID != p.hostID {
		return placementError("directory creation belongs to another installer")
	}
	parents, err := baselineParentPaths()
	if err != nil {
		return err
	}
	if control.Parents == nil {
		control.Parents = map[string]purgeRef{}
	}
	if control.Roots == nil {
		control.Roots = map[string]purgeRef{}
	}
	if control.Refs == nil {
		control.Refs = map[string]purgeRef{}
	}
	for path, ref := range p.installerCreated {
		if path == paths.BinDir() {
			control.Refs["directory:data/bin"] = ref
		}
		for key, target := range parents {
			if path == target {
				control.Parents[key] = ref
			}
		}
		for role, target := range map[string]string{"state": paths.StateDir(), "data": paths.DataDir(), "config": paths.ConfigDir()} {
			if path == target {
				control.Roots[role] = ref
			}
		}
		if path == paths.RuntimeDir() {
			if control.RuntimeRoots == nil {
				control.RuntimeRoots = map[string]runtimeRootReceipt{}
			}
			control.RuntimeRoots[runtimeRootKey(path)] = runtimeRootReceipt{Path: path, Ref: ref, Created: true}
		}
	}
	return nil
}

func (p *placement) createInstallerRootV1(ctx context.Context, path string) (*os.Root, error) {
	if !p.installer {
		return nil, placementError("a normal helper cannot acquire installation directory creation rights")
	}
	if err := p.withControl(ctx, func(control *placementControl) error {
		if control.Drain != "" {
			return UserError("draining", "host removal owns this installation scope", "Finish the original removal before installing a helper.")
		}
		return p.checkIdentity()
	}, false); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRootCreated(path, p.observeInstallerDirectoryV1)
	if err != nil {
		return nil, err
	}
	if err := p.withControl(ctx, func(control *placementControl) error {
		if control.Drain != "" {
			return UserError("draining", "host removal owns this installation scope", "Finish the original removal before installing a helper.")
		}
		return p.commitInstallerDirectoriesV1(control)
	}, true); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}
