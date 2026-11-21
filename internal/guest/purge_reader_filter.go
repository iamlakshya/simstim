package guest

import (
	"fmt"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
)

// This closed reader profile is installed immediately before the one fixed
// Git exec. It cannot create a socket, child, thread, namespace or new group.
// A Git feature needing another helper fails unknown instead of widening it.
var purgeReaderDeniedV1 = []string{
	"socket", "socketpair", "connect", "bind", "listen", "accept", "accept4",
	"fork", "vfork", "clone", "clone3", "setsid", "setpgid", "unshare", "setns",
	"mount", "umount2", "pivot_root", "move_mount", "open_tree", "fsopen", "fsmount", "fsconfig", "fspick", "mount_setattr",
	"ptrace", "process_vm_readv", "process_vm_writev", "pidfd_getfd",
	"keyctl", "add_key", "request_key", "bpf", "perf_event_open", "userfaultfd",
	"io_uring_setup", "io_uring_enter", "io_uring_register", "kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module", "reboot",
}

func purgeReaderFilterV1(goarch string) ([]bpf.RawInstruction, error) {
	native := map[string]uint32{"amd64": 0xc000003e, "arm64": 0xc00000b7}[goarch]
	if native == 0 {
		return nil, purgeWorkErrorV1("the fixed reader has no native syscall profile for this architecture")
	}
	info, err := arch.GetInfo(goarch)
	if err != nil {
		return nil, err
	}
	const kill, refuse, allow = 0x80000000, 0x00050001, 0x7fff0000
	program := []bpf.Instruction{
		bpf.LoadAbsolute{Off: 4, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: native, SkipTrue: 1},
		bpf.RetConstant{Val: kill},
		bpf.LoadAbsolute{Off: 0, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpGreaterOrEqual, Val: 0x40000000, SkipFalse: 1},
		bpf.RetConstant{Val: kill}, // x32/foreign syscall encodings cannot bypass the native table.
	}
	for _, name := range purgeReaderDeniedV1 {
		number, ok := info.SyscallNames[name]
		if !ok { // Some legacy calls do not exist on arm64; their ABI was rejected above.
			continue
		}
		program = append(program, bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(number), SkipFalse: 1}, bpf.RetConstant{Val: refuse}) //nolint:gosec // syscall numbers are nonnegative architecture constants.
	}
	for _, required := range []string{"socket", "socketpair", "connect", "bind", "listen", "clone", "clone3", "unshare", "setns", "setpgid", "setsid", "io_uring_setup", "pidfd_getfd"} {
		if _, ok := info.SyscallNames[required]; !ok {
			return nil, fmt.Errorf("the fixed reader syscall table omits %s", required)
		}
	}
	return bpf.Assemble(append(program, bpf.RetConstant{Val: allow}))
}
