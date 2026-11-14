//go:build linux

package guest

import "syscall"

// vmCommandCredentials narrows the ONE ordinary runner's prepared credentials.
// Only the closed Docker operation may inherit actual guest PID1/root. Seed
// has no Docker socket group even when the later interactive agent may use it.
func vmCommandCredentials(role vmCommandRole, docker bool, attr *syscall.SysProcAttr) error {
	if attr == nil || attr.Pdeathsig != syscall.SIGKILL {
		return vmError("the VM command has no fixed parent-death credential boundary")
	}
	switch role {
	case vmDockerCommand:
		if !docker || attr.Credential != nil {
			return vmError("the guest-root daemon is not the explicit Docker operation")
		}
		return nil
	case vmAgentCommand, vmSeedCommand:
		cred := attr.Credential
		if cred == nil || cred.Uid != AgentUID || cred.Gid != AgentGID || cred.NoSetGroups {
			return vmError("the VM command does not drop to the fixed agent account")
		}
		cred.Groups = []uint32{}
		if role == vmAgentCommand && docker {
			cred.Groups = []uint32{dockerGID}
		}
		return nil
	default:
		return vmError("the VM command role is unknown")
	}
}
