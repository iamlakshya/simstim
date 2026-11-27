package agents

import (
	"fmt"
	"slices"
)

// ForkCommand selects only the canonical agent data. An unrecognised ID
// never enters the fork template; Continue can still use the copied state.
func (a Agent) ForkCommand(id string) ([]string, error) {
	if len(a.Fork) != 0 && a.ValidID(id) {
		return Expand(a.Fork, map[string]string{"agent_session_id": id}), nil
	}
	if len(a.Continue) != 0 {
		return slices.Clone(a.Continue), nil
	}
	return nil, fmt.Errorf("this agent has no fork or continue command. Try: start a new session with its copied files")
}
