package client

import "context"

// State admission belongs to the original ordinary attached connection. The
// separate agent-only process owns the native forwarding binding/first write;
// it can never acquire general value/VM/receipt authority by its role alone.
func checkAgentParent(parent, channel *Conn) error {
	if parent == nil || channel == nil || parent == channel || parent.agentOnly || !channel.agentOnly {
		return agentBindingProofError()
	}
	if err := checkAcceptedHostTransport(parent); err != nil {
		return err
	}
	if err := checkAcceptedHostTransport(channel); err != nil {
		return err
	}
	a, b := parent.hostTransport, channel.hostTransport
	if a.kind != hostTransportSaved || b.kind != hostTransportSaved || !a.source.exists ||
		!sameHostTransportSource(a.source, b.source) || !SameEntry(a.host, b.host) ||
		!ValidHostID(a.hostID) || a.hostID != b.hostID || a.idChanged || b.idChanged {
		return agentBindingProofError()
	}
	return nil
}

func (a *attachedAgent) checkParent() error {
	if a == nil {
		return agentBindingProofError()
	}
	if err := checkAgentParent(a.parent, a.conn); err != nil {
		return err
	}
	if !a.binding.matches(a.conn, a.scope) {
		return agentBindingProofError()
	}
	return nil
}

func (a *attachedAgent) checkLiveGrant(ctx context.Context, session, target string) error {
	if err := a.checkParent(); err != nil {
		return err
	}
	ctx, release, err := a.parent.beginGrant(ctx, "attach", true)
	if err != nil {
		return err
	}
	defer release()
	if err := a.checkParent(); err != nil {
		return err
	}
	return a.parent.checkAgentGrant(ctx, session, target, a.mode.inspector)
}
