//go:build linux

package microvm

import (
	"context"
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
	plan, continuation, err := readFinalExecPlanV1(in)
	if err != nil {
		return err
	}
	if plan.Version != 1 && plan.Version != 2 {
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
	kernel, _ := plan.Spec.VM.Runtime.Find(supply.KindKernel, kind.kernelName(), plan.Spec.VM.Image.Arch)
	program := os.NewFile(3, "VMM-exec")
	guestKernel := os.NewFile(4, "guest-kernel")
	var modeLease, modeWriter *os.File
	if continuation != nil {
		modeLease, modeWriter = os.NewFile(10, "original-vmm-mode-lease"), os.NewFile(11, "original-vmm-final-sentinel")
		if err := supply.CheckProjectedVMMExecModeV1(context.Background(), vmm, program, modeLease); err != nil {
			return err
		}
	} else {
		if err := supply.CheckSealedRuntime(vmm, program); err != nil {
			return err
		}
	}
	if err := supply.CheckRuntimeDescriptor(context.Background(), kernel, guestKernel); err != nil {
		return err
	}
	for _, file := range []*os.File{program, guestKernel} {
		if err := requireReadOnlyRuntimeMount(file); err != nil {
			return err
		}
	}
	if continuation != nil {
		// These are the last program/data O_RDONLY reopens. They finish while
		// the original measured program is 0555, before the private Arm receipt.
		if err := isolateVMMRuntimeOffsetsV1(); err != nil {
			return err
		}
	}
	api, vsock := vmSocketPaths(vmDataDir(plan.Spec))
	var args []string
	if kind.isFirecracker() {
		build := fc.Command
		if kind == nitroBackend {
			build = fc.NitroCommand
		}
		args, _, _, err = build(plan.Spec, fc.Paths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", Config: filepath.Join(plan.Spec.SessionDir, firecrackerConfigName), API: api, Vsock: vsock})
	} else {
		args, _, err = bootCommand(bootSpec{Runtime: plan.Spec.VM.Runtime, Image: plan.Spec.VM.Image, Paths: bootPaths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", API: api, Vsock: vsock}, GuestRAM: plan.Spec.VM.GuestRAM, CPUs: plan.Spec.CPUs, SessionDirect: plan.SessionDirect})
	}
	if err != nil {
		return err
	}
	if continuation != nil {
		// LAST helper barrier: ALL readonly opens/projections/offset isolation
		// above have finished at0555. No later O_RDONLY reopen or intermediate
		// helper exec is permitted after this exact original pipe record.
		// Cmd still requires the original creation/code/namespace/source proof
		// before it may consume this DATA and arm the same original mode lease.
		if err := supply.WriteVMMExecProjectionV1(context.Background(), modeWriter, program, modeLease, vmm); err != nil {
			return err
		}
		if err := waitFinalExecContinueV2(continuation); err != nil {
			return err
		}
		if err := supply.CheckArmedVMMExecModeV1(context.Background(), vmm, program, modeLease); err != nil {
			return err
		}
	}
	// A program descriptor is needed for this execve path, but CLOEXEC
	// closes it at the boundary. Read/control/helper capabilities are never
	// retained. The five boot/data descriptors remain available to the VMM.
	keep := map[int]bool{4: true, 5: true, 6: true, 7: true, 8: true}
	if continuation != nil {
		keep[10], keep[11] = true, true
	}
	if err := closeRuntimeDescriptors(keep); err != nil {
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
	if continuation != nil {
		if err := requireReadOnlyRuntimeMount(program); err != nil {
			return err
		}
		if err := supply.WriteVMMExecArmedV1(context.Background(), modeWriter, program, modeLease, vmm); err != nil {
			return err
		}
		for _, file := range []*os.File{modeLease, modeWriter} {
			if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
				return err
			}
		}
		// No retry, intermediate exec or explicit private-role Close occurs.
		// Kernel would_dump latches before THIS final CLOEXEC boundary.
	}
	execErr := unix.Exec(args[0], args, []string{"LANG=C", "LC_ALL=C", "TZ=UTC"})
	// The Go file objects own these exact raw slots until the kernel exec
	// boundary. Their finalisers must not close the program, kernel or private
	// sentinel early while Exec prepares argv/env or returns a native failure.
	runtime.KeepAlive(program)
	runtime.KeepAlive(guestKernel)
	runtime.KeepAlive(modeLease)
	runtime.KeepAlive(modeWriter)
	if execErr != nil {
		return fmt.Errorf("execute pinned %s: %w", kind.label(), execErr)
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
