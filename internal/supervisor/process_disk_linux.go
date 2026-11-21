//go:build linux

package supervisor

import (
	"errors"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func processDiskResourcesV1(spec wall.Spec) (result *diskResourcesV1, returned error) {
	volume, err := pinDiskVolumeV1(spec.SessionDir)
	if err != nil {
		return nil, err
	}
	resources := &diskResourcesV1{volume: volume, process: true, limit: spec.DiskMax}
	defer func() {
		if returned != nil {
			result = resources
			returned = errors.Join(returned, resources.Close())
		}
	}()
	// Consume the SAME canonical planner used by Start after pinning the
	// private volume. It validates typed state and creates its source folders.
	plan, err := process.MountPlan(spec)
	if err != nil {
		return nil, wallError("cannot identify the prepared process storage", err)
	}
	owned := map[string]string{"/work": filepath.Join(spec.SessionDir, "work")}
	for _, name := range spec.StateDirs {
		owned["/home/agent/"+name] = filepath.Join(spec.SessionDir, "state", filepath.FromSlash(name))
	}
	if len(spec.StateFiles) > 0 {
		owned[files.CapturePath] = spec.StateCapture
	}
	for _, mount := range plan {
		want, known := owned[mount.Guest]
		if !known {
			continue // An external grant or read-only asset is never ownership.
		}
		if !mount.Write || mount.Host != want {
			return nil, diskFailure("disk_measure", "the process planner changed an owned writable storage role")
		}
		root, err := resources.pinOwnedRootV1(volume, spec.SessionDir, mount.Host)
		if err != nil {
			return nil, err
		}
		// Validate execution-source roles without narrowing the accounting
		// root. Inactive caches and read-only private epochs still use space.
		if err := files.ValidateDiskRoots(volume, root); err != nil {
			return nil, wallError("cannot validate a process storage source", err)
		}
		delete(owned, mount.Guest)
	}
	if len(owned) != 0 {
		return nil, diskFailure("disk_measure", "the process planner omitted an owned writable storage role")
	}
	// Spec 17.1 charges the WHOLE actual private session folder. Measuring
	// only active binds would omit retained caches after a drop/rotation.
	root, err := volume.OpenRoot(".")
	if err != nil {
		return nil, wallError("cannot pin the complete private session storage", err)
	}
	resources.roots = append(resources.roots, root)
	if err := files.ValidateDiskRoots(volume, resources.roots...); err != nil {
		return nil, wallError("cannot validate the process factory's pinned disk roots", err)
	}
	return resources, nil
}
