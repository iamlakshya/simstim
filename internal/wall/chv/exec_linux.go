//go:build linux

package chv

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// RunVMMExec is the final closed external-exec stage. It leaves only the
// kernel/disks and ordinary stdio available to the unmodified pinned VMM.
func RunVMMExec(in io.Reader) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := helperNoDump(); err != nil {
		return err
	}
	var plan runnerPlan
	if err := decodeHelper(in, &plan); err != nil {
		return err
	}
	if plan.Version != 1 {
		return fmt.Errorf("unknown VMM execution version")
	}
	if err := validateVMSpec(plan.Spec); err != nil {
		return err
	}
	if err := helperUID(plan.UIDMode); err != nil {
		return err
	}
	vmm, _ := plan.Spec.VM.Runtime.Find(supply.KindVMM, "cloud-hypervisor", plan.Spec.VM.Image.Arch)
	kernel, _ := plan.Spec.VM.Runtime.Find(supply.KindKernel, "guest", plan.Spec.VM.Image.Arch)
	program := os.NewFile(3, "VMM-exec")
	guestKernel := os.NewFile(4, "guest-kernel")
	if err := supply.CheckSealedRuntime(vmm, program); err != nil {
		return err
	}
	if err := supply.CheckSealedRuntime(kernel, guestKernel); err != nil {
		return err
	}
	api, vsock := vmSocketPaths(vmDataDir(plan.Spec))
	args, _, err := bootCommand(bootSpec{Runtime: plan.Spec.VM.Runtime, Image: plan.Spec.VM.Image, Paths: bootPaths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", API: api, Vsock: vsock}, GuestRAM: plan.Spec.VM.GuestRAM, CPUs: plan.Spec.CPUs, SessionDirect: plan.SessionDirect})
	if err != nil {
		return err
	}
	// A program descriptor is needed for this execve path, but CLOEXEC
	// closes it at the boundary. Read/control/helper capabilities are never
	// retained. The five boot/data descriptors remain available to the VMM.
	if err := closeRuntimeDescriptors(map[int]bool{4: true, 5: true, 6: true, 7: true, 8: true}); err != nil {
		return err
	}
	unix.CloseOnExec(3)
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("replace VMM metadata input: %w", err)
	}
	writer.Close()
	if err := unix.Dup3(int(reader.Fd()), 0, 0); err != nil {
		reader.Close()
		return fmt.Errorf("close VMM metadata channel: %w", err)
	}
	reader.Close()
	if err := unix.Exec(args[0], args, []string{"LANG=C", "LC_ALL=C", "TZ=UTC"}); err != nil {
		return fmt.Errorf("execute pinned Cloud Hypervisor: %w", err)
	}
	return nil
}
