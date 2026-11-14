//go:build linux

package guest

import (
	"syscall"
	"testing"
)

func TestVMOnlyExplicitDockerOperationCanRunAsGuestRoot(t *testing.T) {
	for _, role := range []vmCommandRole{vmAgentCommand, vmSeedCommand, vmDockerCommand, 99} {
		for _, docker := range []bool{false, true} {
			attr := &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			err := vmCommandCredentials(role, docker, attr)
			if (err == nil) != (role == vmDockerCommand && docker) {
				t.Fatal("guest-root command authority changed", role, docker, err)
			}
		}
	}
}

func TestVMSeedNeverGetsDockerGroupAndInteractiveAgentGetsOnlyExplicit2000(t *testing.T) {
	for _, role := range []vmCommandRole{vmAgentCommand, vmSeedCommand} {
		for _, docker := range []bool{false, true} {
			attr := &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Credential: &syscall.Credential{Uid: 1000, Gid: 1000, Groups: []uint32{17, 999}}}
			if err := vmCommandCredentials(role, docker, attr); err != nil {
				t.Fatal(err)
			}
			if role == vmAgentCommand && docker {
				if len(attr.Credential.Groups) != 1 || attr.Credential.Groups[0] != 2000 {
					t.Fatal("interactive Docker group omitted or widened")
				}
			} else if len(attr.Credential.Groups) != 0 {
				t.Fatal("seed/default command inherited privileged groups")
			}
		}
	}
}
