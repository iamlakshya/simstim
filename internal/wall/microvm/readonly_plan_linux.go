//go:build linux

package microvm

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/supply"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
)

// Compile DATA only from the original already admitted plan. The private Cmd
// still binds its original actual store source, two-map creation and programme.
func readonlyInitPlanV1(plan runnerPlan) (jail.VMMReadonlyInitPlanV1, error) {
	var result jail.VMMReadonlyInitPlanV1
	if plan.Version != 3 || plan.UIDMode != "subuid" && plan.UIDMode != "login" {
		return result, fmt.Errorf("read-only direct init requires its original two-map VMM plan")
	}
	if err := validateVMSpec(plan.Spec); err != nil {
		return result, err
	}
	kind, err := backendForSpec(plan.Spec)
	if err != nil {
		return result, err
	}
	program, ok := plan.Spec.VM.Runtime.Find(supply.KindVMM, kind.vmmName(), plan.Spec.VM.Image.Arch)
	if !ok {
		return result, fmt.Errorf("the original plan lost its selected VMM")
	}
	kernel, ok := plan.Spec.VM.Runtime.Find(supply.KindKernel, "guest", plan.Spec.VM.Image.Arch)
	if !ok {
		return result, fmt.Errorf("the original plan lost its selected kernel")
	}
	api, vsock := vmSocketPaths(vmDataDir(plan.Spec))
	var args []string
	if kind == firecrackerBackend {
		args, _, _, err = fc.Command(plan.Spec, fc.Paths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", Config: filepath.Join(plan.Spec.SessionDir, firecrackerConfigName), API: api, Vsock: vsock})
	} else if kind == cloudHypervisorBackend {
		args, _, err = bootCommand(bootSpec{Runtime: plan.Spec.VM.Runtime, Image: plan.Spec.VM.Image, Paths: bootPaths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", API: api, Vsock: vsock}, GuestRAM: plan.Spec.VM.GuestRAM, CPUs: plan.Spec.CPUs, SessionDirect: plan.SessionDirect})
	} else {
		return result, fmt.Errorf("this backend has no original two-map read-only init producer")
	}
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return result, err
	}
	budget, err := BudgetFor(plan.Spec.VM.GuestRAM)
	if err != nil {
		return result, err
	}
	result = jail.VMMReadonlyInitPlanV1{Program: program, Kernel: kernel, Argv: args, Runtime: raw, VMDir: vmDataDir(plan.Spec), DiskMax: plan.Spec.DiskMax, MemoryMax: budget.Limit, API: api, Vsock: vsock}
	return result, nil
}
