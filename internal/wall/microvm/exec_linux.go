//go:build linux

package microvm

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/iamlakshya/simstim/internal/supply"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
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
	kind, err := backendForSpec(plan.Spec)
	if err != nil {
		return err
	}
	vmm, _ := plan.Spec.VM.Runtime.Find(supply.KindVMM, kind.vmmName(), plan.Spec.VM.Image.Arch)
	kernel, _ := plan.Spec.VM.Runtime.Find(supply.KindKernel, "guest", plan.Spec.VM.Image.Arch)
	program := os.NewFile(3, "VMM-exec")
	guestKernel := os.NewFile(4, "guest-kernel")
	if err := supply.CheckSealedRuntime(vmm, program); err != nil {
		return err
	}
	if err := supply.CheckSealedRuntime(kernel, guestKernel); err != nil {
		return err
	}
	for _, file := range []*os.File{program, guestKernel} {
		if err := requireReadOnlyRuntimeMount(file); err != nil {
			return err
		}
	}
	api, vsock := vmSocketPaths(vmDataDir(plan.Spec))
	var args []string
	if kind == firecrackerBackend {
		args, _, _, err = fc.Command(plan.Spec, fc.Paths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", Config: filepath.Join(plan.Spec.SessionDir, firecrackerConfigName), API: api, Vsock: vsock})
	} else {
		args, _, err = bootCommand(bootSpec{Runtime: plan.Spec.VM.Runtime, Image: plan.Spec.VM.Image, Paths: bootPaths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", API: api, Vsock: vsock}, GuestRAM: plan.Spec.VM.GuestRAM, CPUs: plan.Spec.CPUs, SessionDirect: plan.SessionDirect})
	}
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
		return fmt.Errorf("execute pinned %s: %w", kind.label(), err)
	}
	return nil
}

// A read-only open does not stop fchmod or a writable procfd reopen. The
// namespace init replaces each selected handle through its own read-only
// bind mount. Require that mount before an unmodified VMM inherits the files.
func requireReadOnlyRuntimeMount(file *os.File) error {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect runtime descriptor mount: %w", err)
	}
	if stat.Flags&unix.ST_RDONLY == 0 {
		return fmt.Errorf("runtime execution needs its checked read-only descriptor mount")
	}
	return nil
}
