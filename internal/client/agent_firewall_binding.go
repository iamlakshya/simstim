package client

import (
	"errors"
	"fmt"
)

type agentSourceFailure struct{ cause error }

func (e *agentSourceFailure) Error() string { return e.cause.Error() }
func (e *agentSourceFailure) Unwrap() error { return e.cause }

func newAgentSourceFailure(message string, cause error) error {
	if cause == nil {
		return &agentSourceFailure{cause: errors.New(message)}
	}
	return &agentSourceFailure{cause: fmt.Errorf("%s: %w", message, cause)}
}

func isAgentSourceFailure(err error) bool {
	var source *agentSourceFailure
	return errors.As(err, &source)
}

func checkAgentFirewallBinding(binding *peerAgentBinding) error {
	if binding == nil || !binding.verified || binding.owner == nil || binding.scope.Check() != nil || len(binding.wire) < 5 {
		return newAgentSourceFailure("a verified outer agent binding is required", nil)
	}
	wire := binding.wire
	n := agentFirewallUint32(wire[:4])
	if n == 0 || n > agentFWMaxBody || int(n) != len(wire)-4 || wire[4] != agentFWExtension {
		return newAgentSourceFailure("invalid verified agent binding frame", nil)
	}
	name, rest, ok := takeAgentFirewallString(wire[5:])
	if !ok || string(name) != agentFWSessionBind {
		return newAgentSourceFailure("invalid verified agent binding extension", nil)
	}
	for range 3 { // Host key, session ID and signature. Crypto is collector-owned.
		var value []byte
		value, rest, ok = takeAgentFirewallString(rest)
		if !ok || len(value) == 0 {
			return newAgentSourceFailure("incomplete verified agent binding", nil)
		}
	}
	if len(rest) != 1 || rest[0] != 1 {
		return newAgentSourceFailure("the verified agent binding is not a forwarding prefix", nil)
	}
	return nil
}

func takeAgentFirewallString(b []byte) (value, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := agentFirewallUint32(b[:4])
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, false
	}
	end := 4 + int(n)
	return b[4:end], b[end:], true
}

func (f *agentFirewall) bindSource(wire []byte) error {
	// The immutable record is shared once per lease, not copied per stream.
	for len(wire) > 0 {
		n := min(len(wire), agentFWBufferSize)
		if err := writeAgentFirewallBytes(f.source, wire[:n]); err != nil {
			return newAgentSourceFailure("write verified agent binding", err)
		}
		wire = wire[n:]
	}
	_, length, opcode, err := readAgentFirewallHead(f.source)
	if err != nil {
		return newAgentSourceFailure("read verified agent binding acknowledgement", err)
	}
	if length != 1 || opcode != agentFWSuccess {
		return newAgentSourceFailure("the source agent did not accept the verified outer binding", nil)
	}
	return nil
}
