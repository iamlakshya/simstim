package jail

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// CommandGvisorV1 builds the closed userspace-kernel bootstrap purpose. Its
// caller is the host's original native boot owner. Every source is an actual
// supplied descriptor, and the outer private PID 1 loses all capabilities
// before it starts a trusted exec stage in a child user and mount namespace.
func CommandGvisorV1(ctx context.Context, spec Spec) (*Cmd, error) {
	if spec.Subuid || spec.Net != NetNone || spec.Seccomp != "" || len(spec.Binds) == 0 {
		return nil, failure("the gvisor command has conflicting namespace policy", "Use the original host gvisor factory.")
	}
	for _, bound := range spec.Binds {
		if bound.File == nil {
			return nil, failure("gvisor needs each original captured mount capability", "Pass the original runtime and filesystem sources without reopening their paths.")
		}
	}
	spec.gvisorV1, spec.Seccomp = true, profileGvisorV1
	return Command(ctx, spec)
}

// gvisorBootstrapCapsV1 is confined to the child user namespace. No capability
// is retained by the outer PID 1, and no additional host UID or GID is mapped.
var gvisorBootstrapCapsV1 = []uintptr{
	unix.CAP_CHOWN, unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH, unix.CAP_FOWNER,
	unix.CAP_FSETID, unix.CAP_SETGID, unix.CAP_SETUID, unix.CAP_SETPCAP,
	unix.CAP_SYS_CHROOT, unix.CAP_SYS_ADMIN,
}

func gvisorExecNamespacesV1() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags:  unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: 1}},
		AmbientCaps: append([]uintptr(nil), gvisorBootstrapCapsV1...),
		Pdeathsig:   unix.SIGKILL,
	}
}

func prepareGvisorBootstrapV1() error {
	ruid, euid, suid := unix.Getresuid()
	rgid, egid, sgid := unix.Getresgid()
	if os.Getppid() != 1 || ruid != 0 || euid != 0 || suid != 0 || rgid != 0 || egid != 0 || sgid != 0 {
		return failure("the gvisor exec stage lost its original private PID 1 or single mapped identity", "Keep the original native namespace owner.")
	}
	for _, name := range []string{"uid_map", "gid_map"} {
		data, err := os.ReadFile("/proc/self/" + name)
		if err != nil || !singleGvisorMapV1(string(data)) {
			return errors.Join(failure("the gvisor exec stage maps another identity", "Use only the original login user's single mapping."), err)
		}
	}
	var permitted uint64
	for _, capability := range gvisorBootstrapCapsV1 {
		permitted |= uint64(1) << capability
	}
	for capability := range uintptr(64) {
		if permitted&(uint64(1)<<capability) != 0 {
			continue
		}
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, capability, 0, 0, 0); errors.Is(err, unix.EINVAL) {
			break
		} else if err != nil {
			return stepFailed("cannot limit the nested gvisor capability set", "Check this host's namespace confinement support.", err)
		}
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{
		{Effective: uint32(permitted & 0xffffffff), Permitted: uint32(permitted & 0xffffffff), Inheritable: uint32(permitted & 0xffffffff)},
		{Effective: uint32(permitted >> 32), Permitted: uint32(permitted >> 32), Inheritable: uint32(permitted >> 32)},
	}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return stepFailed("cannot apply the nested gvisor capability set", "Keep the original namespace setup.", err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return stepFailed("cannot protect the nested gvisor bootstrap", "Check this host's process confinement support.", err)
	}
	got, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || got != 0 {
		return errors.Join(failure("the nested gvisor bootstrap is dumpable", "Keep the original native namespace owner."), err)
	}
	return nil
}

func singleGvisorMapV1(text string) bool {
	if len(text) > 128 || !strings.HasSuffix(text, "\n") || strings.Count(text, "\n") != 1 {
		return false
	}
	fields := strings.Fields(text)
	if len(fields) != 3 {
		return false
	}
	for _, value := range []byte(text) {
		if value != ' ' && value != '\t' && value != '\n' && (value < '0' || value > '9') {
			return false
		}
	}
	return fields[0] == "0" && fields[1] == "0" && fields[2] == "1"
}
