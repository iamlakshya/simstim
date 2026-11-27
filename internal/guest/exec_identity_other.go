//go:build !linux

package guest

import (
	"fmt"
	"syscall"
)

// AgentUID and AgentGID are the production Linux guest account ABI.
const AgentUID, AgentGID = 1000, 1000

func agentProcess() (*syscall.SysProcAttr, error) {
	return nil, fmt.Errorf("agent commands require a Linux guest. Try: use a Linux wall host")
}
