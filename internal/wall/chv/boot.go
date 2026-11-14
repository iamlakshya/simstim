package chv

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

type bootPaths struct{ VMM, Kernel, Tools, Image, Agents, Session, API, Vsock string }
type bootSpec struct {
	Runtime       supply.Manifest
	Image         image.Request
	Paths         bootPaths
	GuestRAM      int64
	CPUs          int
	SessionDirect bool
}

func bootCommand(spec bootSpec) ([]string, MemoryBudget, error) {
	if err := validateBootRuntime(spec.Runtime, spec.Image.Arch); err != nil {
		return nil, MemoryBudget{}, err
	}
	if err := spec.Image.Validate(); err != nil {
		return nil, MemoryBudget{}, err
	}
	if spec.Image.RootHash == "" || spec.Image.DataBlocks <= 0 {
		return nil, MemoryBudget{}, fmt.Errorf("the boot image has no client root check")
	}
	vmm, ok := spec.Runtime.Find(supply.KindVMM, "cloud-hypervisor", spec.Image.Arch)
	if !ok || supply.Below(vmm.Version, "54.0") {
		return nil, MemoryBudget{}, fmt.Errorf("the microvm runtime needs Cloud Hypervisor54.0 or later with main-thread seccomp")
	}
	if found := spec.Runtime.OwnFindings(); len(found) > 0 {
		return nil, MemoryBudget{}, fmt.Errorf("the client runtime matches an advisory: %s", found[0].String())
	}
	tools, ok := spec.Runtime.Find(supply.KindTools, "tools", spec.Image.Arch)
	if !ok {
		return nil, MemoryBudget{}, fmt.Errorf("the client runtime has no tools image")
	}
	if _, ok := spec.Runtime.Find(supply.KindKernel, "guest", spec.Image.Arch); !ok {
		return nil, MemoryBudget{}, fmt.Errorf("the client runtime has no guest kernel")
	}
	budget, err := BudgetFor(spec.GuestRAM)
	if err != nil {
		return nil, MemoryBudget{}, err
	}
	if spec.CPUs <= 0 {
		return nil, MemoryBudget{}, fmt.Errorf("the guest needs a positive normalised CPU count")
	}
	p := spec.Paths
	for _, path := range []string{p.VMM, p.Kernel, p.Tools, p.Image, p.Agents, p.Session, p.API, p.Vsock} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, ",\x00\r\n") {
			return nil, MemoryBudget{}, fmt.Errorf("a VMM resource path is not an absolute clean option-safe path")
		}
	}
	if len(p.API) > 107 || len(p.Vsock) > 107 {
		return nil, MemoryBudget{}, fmt.Errorf("the VMM private socket path exceeds the Unix socket address limit")
	}
	args := []string{p.VMM, "--api-socket", "path=" + p.API, "--kernel", p.Kernel, "--cmdline", kernelCommandLine(tools, spec.Image), "--cpus", "boot=" + strconv.Itoa(spec.CPUs), "--memory", fmt.Sprintf("size=%dM", budget.Guest/mebibyte), "--balloon", "size=0,free_page_reporting=on", "--disk"}
	for _, disk := range []struct{ path, serial string }{{p.Tools, "tools"}, {p.Image, "image"}, {p.Agents, "agents"}} {
		args = append(args, "path="+disk.path+",readonly=on,direct=on,image_type=raw,backing_files=off,serial="+disk.serial)
	}
	direct := "on"
	if !spec.SessionDirect {
		direct = "off"
	}
	args = append(args, "path="+p.Session+",readonly=off,direct="+direct+",image_type=raw,backing_files=off,sparse=on,serial=session", "--vsock", "cid=3,socket="+p.Vsock, "--serial", "off", "--console", "off", "--seccomp", "true", "--landlock")
	return args, budget, nil
}

// The caller has already validated these complete canonical runtime/image
// items. No guest-provided string enters the device-mapper tables.
func kernelCommandLine(tools supply.Item, root image.Request) string {
	return fmt.Sprintf("ro quiet root=/dev/dm-0 rootfstype=erofs init=/init dm-mod.waitfor=/dev/vda,/dev/vdb dm-mod.create=\"tools,,0,ro,0 %d verity 1 /dev/vda /dev/vda 4096 4096 %d %d sha256 %s %s 1 panic_on_corruption;image,,1,ro,0 %d verity 1 /dev/vdb /dev/vdb 4096 4096 %d %d sha256 %s %s 1 panic_on_corruption\" -- wall-init --microvm-v1", tools.DataBlocks*8, tools.DataBlocks, tools.DataBlocks, tools.RootHash, tools.Salt, root.DataBlocks*8, root.DataBlocks, root.DataBlocks+1, root.RootHash, tools.Salt)
}
