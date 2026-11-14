//go:build linux

package microvm

import (
	"context"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

type bootFiles struct {
	vmm, kernel, tools, image, agents, helper *os.File
	imageLease                                *image.Lease
	agentsLease                               *image.Lease
}

func (f *bootFiles) close() {
	for _, file := range []*os.File{f.vmm, f.kernel, f.tools, f.image, f.agents, f.helper} {
		if file != nil {
			file.Close()
		}
	}
	if f.imageLease != nil {
		f.imageLease.Close()
	}
	if f.agentsLease != nil {
		f.agentsLease.Close()
	}
}

func (d *Driver) openBootFiles(ctx context.Context, spec wall.Spec) (files *bootFiles, returned error) {
	files = &bootFiles{}
	defer func() {
		if returned != nil {
			files.close()
		}
	}()
	domain, err := d.options.Domain(ctx)
	if err != nil {
		return files, err
	}
	store, err := runtimecache.OpenHostReadOnly(ctx, d.options.Artefacts, domain)
	if err != nil {
		return files, err
	}
	defer store.Close()
	for _, role := range bootRoles {
		item, ok := spec.VM.Runtime.Find(role.Kind, role.Name, spec.VM.Image.Arch)
		if !ok {
			return files, fmt.Errorf("client runtime is missing %s", role.Kind)
		}
		file, err := store.OpenItem(ctx, item)
		if err != nil {
			return files, err
		}
		switch role.Kind {
		case supply.KindVMM:
			files.vmm, err = executeOnlyVMM(ctx, item, file)
			file.Close()
		case supply.KindKernel:
			files.kernel, err = supply.SealRuntime(ctx, item, file)
			file.Close()
		case supply.KindTools:
			files.tools = file
		}
		if err != nil {
			return files, err
		}
	}
	images, err := image.OpenHostExisting(ctx, d.options.Images, domain)
	if err != nil {
		return files, err
	}
	files.image, files.imageLease, _, err = images.OpenImageExisting(ctx, spec.VM.Image)
	defer images.Close()
	if err != nil {
		return files, err
	}
	files.agents, files.agentsLease, err = images.OpenAgentsV1(ctx, spec.VM.Agents)
	if err != nil {
		return files, err
	}
	files.helper, err = os.Open("/proc/self/exe")
	if err != nil {
		return files, fmt.Errorf("pin current microvm helper: %w", err)
	}
	return files, nil
}
