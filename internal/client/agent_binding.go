package client

import "github.com/iamlakshya/simstim/internal/agentbridge"

// peerAgentBinding is sealed to one owned connection and attachment lease.
// Only a verified outer forwarding record may authorise source requests.
// A missing proof never falls back to an unbound source connection.
type peerAgentBinding struct {
	parent   *Conn
	wire     []byte
	owner    *Conn
	scope    agentbridge.Scope
	verified bool
}

func (b *peerAgentBinding) matches(owner *Conn, scope agentbridge.Scope) bool {
	if b == nil || owner == nil || !b.verified || b.owner != owner || b.scope != scope || len(b.wire) == 0 {
		return false
	}
	if b.parent != nil {
		if err := checkAgentParent(b.parent, owner); err != nil {
			return false
		}
	}
	if process, ok := owner.proc.(interface{ rawExited() <-chan struct{} }); ok {
		select {
		case <-process.rawExited():
			return false
		default:
		}
	}
	select {
	case <-owner.Done():
		return false
	default:
		return true
	}
}
