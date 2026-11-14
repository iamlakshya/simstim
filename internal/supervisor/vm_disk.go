package supervisor

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/wall"
)

func wrapVMRuntimeV1(owner context.Context, runtime wall.Sandbox) (sandbox, error) {
	spec, known := wall.ExpectedSpec(owner)
	if !known {
		err := diskFailure("disk_measure", "the VM factory lost its trusted expected storage plan")
		if release, ok := runtime.(interface{ ReleasePreparedV1() error }); ok {
			err = errors.Join(err, release.ReleasePreparedV1())
		}
		return nil, err
	}
	resources, err := vmDiskResourcesV1(spec, runtime)
	if err != nil {
		return nil, err
	}
	return vmRuntime{Sandbox: runtime, disk: resources}, nil
}

func vmDiskResourcesV1(spec wall.Spec, runtime wall.Sandbox) (*diskResourcesV1, error) {
	release, ok := runtime.(interface{ ReleasePreparedV1() error })
	if !ok {
		return nil, diskFailure("disk_measure", "the VM factory has no owned prepared-resource release")
	}
	volume, err := pinDiskVolumeV1(spec.SessionDir)
	if err != nil {
		return nil, errors.Join(err, release.ReleasePreparedV1())
	}
	// A VM enforces its exact disk_max through the existing disk factory.
	// Host observation checks the pinned backing filesystem's free space;
	// it never measures the raw sparse file's apparent byte length.
	return &diskResourcesV1{volume: volume, release: release.ReleasePreparedV1}, nil
}
