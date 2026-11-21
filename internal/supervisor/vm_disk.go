package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/wall"
)

func wrapVMRuntimeV1(owner context.Context, runtime wall.Sandbox) (sandbox, error) {
	spec, known := wall.ExpectedSpec(owner)
	if !known {
		err := diskFailure("disk_measure", "the VM factory lost its trusted expected storage plan")
		return &failedSandboxPreparationV1{runtime: runtime}, err
	}
	resources, err := vmDiskResourcesV1(spec, runtime)
	if err != nil {
		return &failedSandboxPreparationV1{runtime: runtime, disk: resources}, err
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
		// This partial object owns only cleanup of the SAME actual runtime.
		// Its absent volume cannot produce disk options or start permission.
		return &diskResourcesV1{release: release.ReleasePreparedV1}, err
	}
	// A VM enforces its exact disk_max through the existing disk factory.
	// Host observation checks the pinned backing filesystem's free space;
	// it never measures the raw sparse file's apparent byte length.
	return &diskResourcesV1{volume: volume, release: release.ReleasePreparedV1}, nil
}
