//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

type bootFiles struct {
	closeMu                                   sync.Mutex
	retirements                               map[*os.File]*microVMFileRetirementV2
	runtimeFiles                              []*os.File
	runtimeStore                              *runtimecache.Store
	imageStore                                *image.Store
	diagnostics                               error
	vmmOwner                                  *supply.VerifiedRuntime // SAME original physical runtime owner
	runtimeOwners                             []*supply.VerifiedRuntime
	borrowedRuntime                           bool
	vmm, kernel, tools, image, agents, helper *os.File
	imageLease                                *image.Lease
	agentsLease                               io.Closer
}

func (f *bootFiles) close() error {
	if f == nil {
		return nil
	}
	f.closeMu.Lock()
	defer f.closeMu.Unlock()
	if f.retirements == nil {
		f.retirements = map[*os.File]*microVMFileRetirementV2{}
	}
	for _, file := range []**os.File{&f.tools, &f.image, &f.agents, &f.helper} {
		if err := closeMicroVMFileV2(file, f.retirements, &f.diagnostics); err != nil {
			return err
		}
	}
	if !f.borrowedRuntime {
		for _, file := range []**os.File{&f.vmm, &f.kernel} {
			if err := closeMicroVMFileV2(file, f.retirements, &f.diagnostics); err != nil {
				return err
			}
		}
	}
	if err := closeMicroVMRuntimeOwnersV2(&f.runtimeOwners, &f.runtimeFiles, f.retirements, &f.diagnostics); err != nil {
		return err
	}
	if f.imageLease != nil {
		if err := f.imageLease.Close(); err != nil {
			return err
		}
		f.imageLease = nil
	}
	if f.agentsLease != nil {
		if err := f.agentsLease.Close(); err != nil {
			return err
		}
		f.agentsLease = nil
	}
	if f.imageStore != nil {
		if err := f.imageStore.Close(); err != nil {
			return err
		}
		f.imageStore = nil
	}
	if f.runtimeStore != nil {
		if err := f.runtimeStore.Close(); err != nil {
			return err
		}
		f.runtimeStore = nil
	}
	f.vmm, f.kernel, f.vmmOwner = nil, nil, nil
	return nil
}

func (d *Driver) openBootFiles(ctx context.Context, spec wall.Spec) (files *bootFiles, returned error) {
	files = &bootFiles{}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, files.close())
		}
	}()
	domain, err := d.options.Domain(ctx)
	if err != nil {
		return files, err
	}
	store, err := runtimecache.OpenHostExecution(ctx, d.options.Artefacts, domain)
	files.runtimeStore = store
	if err != nil {
		return files, err
	}
	for _, role := range d.kind.roles() {
		item, ok := spec.VM.Runtime.Find(role.Kind, role.Name, spec.VM.Image.Arch)
		if !ok {
			return files, fmt.Errorf("client runtime is missing %s", role.Kind)
		}

		switch role.Kind {
		case supply.KindVMM, supply.KindKernel:
			if role.Kind == supply.KindVMM {
				if err := noDumpPrerequisite(); err != nil {
					return files, err
				}
			}
			owner, prepareErr := store.PrepareRuntime(ctx, item)
			if owner != nil {
				files.runtimeOwners = append(files.runtimeOwners, owner)
				files.borrowedRuntime = true
			}
			if prepareErr != nil {
				return files, prepareErr
			}
			borrowed, fileErr := owner.File()
			if fileErr != nil {
				return files, fileErr
			}
			if role.Kind == supply.KindVMM {
				files.vmm = borrowed
				files.vmmOwner = owner
			} else {
				files.kernel = borrowed
			}
		case supply.KindTools:
			files.tools, err = store.OpenItem(ctx, item)
		}
		if err != nil {
			return files, err
		}
	}
	images, err := image.OpenHostExisting(ctx, d.options.Images, domain)
	files.imageStore = images
	if err != nil {
		return files, err
	}
	files.image, files.imageLease, _, err = images.OpenImageExisting(ctx, spec.VM.Image)
	if err != nil {
		return files, err
	}
	files.agents, files.agentsLease, err = images.OpenAgentsV1(ctx, spec.VM.Agents)
	if err != nil {
		return files, err
	}
	files.agentsLease = &bootRuntimeLeaseV1{agents: files.agentsLease, image: files.imageLease, runtime: files.runtimeOwners, runtimeStore: files.runtimeStore, imageStore: files.imageStore}
	files.runtimeOwners = nil
	files.imageLease = nil
	files.runtimeStore, files.imageStore = nil, nil
	files.helper, err = os.Open("/proc/self/exe")
	if err != nil {
		return files, fmt.Errorf("pin current microvm helper: %w", err)
	}
	return files, nil
}
