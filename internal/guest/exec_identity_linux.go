package guest

import (
	"fmt"
	"os"
	"syscall"
)

// AgentUID and AgentGID are the fixed guest account ABI (spec 3.4/7.1).
const AgentUID, AgentGID = 1000, 1000

// agentProcess never inherits trusted init root. Root installs exactly the
// fixed agent credentials and clears supplementary groups. An already-agent
// init is accepted only without foreign supplementary groups.
func agentProcess() (*syscall.SysProcAttr, error) {
	attr := &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if os.Geteuid() == 0 {
		attr.Credential = &syscall.Credential{Uid: AgentUID, Gid: AgentGID, Groups: []uint32{}}
		return attr, nil
	}
	if os.Geteuid() != AgentUID || os.Getegid() != AgentGID {
		return nil, fmt.Errorf("the guest init cannot spawn the agent account. Try: update the guest image and helper")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return nil, err
	}
	for _, gid := range groups {
		if gid != AgentGID {
			return nil, fmt.Errorf("the guest agent inherited an unapproved group. Try: update the guest image and helper")
		}
	}
	return attr, nil
}
