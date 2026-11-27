//go:build linux

package guest

import "syscall"

// vmCommandCredentials narrows the ONE ordinary runner's prepared credentials.
// Only fixed private Docker and file-publication entry roles may begin as guest root. The file publisher drops to1000 before destination IO. Seed
// has no Docker socket group even when the later interactive agent may use it.
func vmCommandCredentials(role vmCommandRole, docker bool, attr *syscall.SysProcAttr) error {
	if attr == nil || attr.Pdeathsig != syscall.SIGKILL {
		return vmError("the VM command has no fixed parent-death credential boundary")
	}
	switch role {
	case vmFileCommitCommand:
		if attr.Credential != nil {
			return vmError("the private publisher did not begin in its original root descriptor owner")
		}
		return nil
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
