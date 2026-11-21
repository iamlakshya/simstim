//go:build linux

package jail

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

func TestVMMOuterActualIssuerRejectsCreationTransformationsV1(t *testing.T) {
	// These are pure attribute falsifiers. No native receipt or process owner
	// is constructed from this data; actual issuance still needs the original
	// synchronous self-exec, atomic creation handle and retained native objects.
	if err := checkVMMOuterAttrsV1(&syscall.SysProcAttr{Setsid: true}, -1, false); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*syscall.SysProcAttr){
		func(value *syscall.SysProcAttr) { value.Setsid = false },
		func(value *syscall.SysProcAttr) { value.Chroot = "/different" },
		func(value *syscall.SysProcAttr) { value.Credential = &syscall.Credential{Uid: 1} },
		func(value *syscall.SysProcAttr) { value.Ptrace = true },
		func(value *syscall.SysProcAttr) { value.Setpgid = true },
		func(value *syscall.SysProcAttr) { value.Setctty = true },
		func(value *syscall.SysProcAttr) { value.Noctty = true },
		func(value *syscall.SysProcAttr) { value.Ctty = 1 },
		func(value *syscall.SysProcAttr) { value.Foreground = true },
		func(value *syscall.SysProcAttr) { value.Pgid = 1 },
		func(value *syscall.SysProcAttr) { value.Cloneflags = 1 },
		func(value *syscall.SysProcAttr) { value.Unshareflags = 1 },
		func(value *syscall.SysProcAttr) {
			value.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: 1, Size: 1}}
		},
		func(value *syscall.SysProcAttr) {
			value.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: 1, Size: 1}}
		},
		func(value *syscall.SysProcAttr) { value.GidMappingsEnableSetgroups = true },
		func(value *syscall.SysProcAttr) { value.AmbientCaps = []uintptr{1} },
		func(value *syscall.SysProcAttr) { value.UseCgroupFD = true },
		func(value *syscall.SysProcAttr) { value.CgroupFD = 1 },
		func(value *syscall.SysProcAttr) { fd := 1; value.PidFD = &fd },
	} {
		value := syscall.SysProcAttr{Setsid: true}
		change(&value)
		if err := checkVMMOuterAttrsV1(&value, -1, false); err == nil {
			t.Fatal("a transformed outer creation entered the closed self-exec issuer")
		}
	}
}
func TestVMMOuterReceiptCannotBeIssuedBeforeActualCreationV1(t *testing.T) {
	var owner vmmExecOwnerV1
	if err := owner.issueOuterExecReceiptV1(&Cmd{}, &vmmExecActorV1{}, 1); err == nil {
		t.Fatal("data fields issued an original outer execution receipt")
	}
	if err := owner.checkHelperActorV1(nil, [3]os.FileInfo{}); err == nil {
		t.Fatal("a missing actor acquired an outer receipt proof")
	}
}

func TestVMMOuterIssuerRefusesInheritedEnvironmentV1(t *testing.T) {
	command := &Cmd{files: make([]*os.File, 9), cmd: &exec.Cmd{Path: self, Args: []string{argv0, stageOuterVMMV1, strconv.Itoa(12)}, Env: nil, SysProcAttr: &syscall.SysProcAttr{Setsid: true}}}
	if err := checkVMMOuterCommandV1(command, -1, false); err == nil {
		t.Fatal("nil inherited environment entered the exact empty-env self-exec issuer")
	}
	command.cmd.Env = []string{}
	if err := checkVMMOuterCommandV1(command, -1, false); err != nil {
		t.Fatal("the actual canonical explicit empty environment was refused", err)
	}
}
