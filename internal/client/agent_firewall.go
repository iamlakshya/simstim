package client

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	// These are OpenSSH's AGENT_MAX_LEN and MAX_AGENT_REPLY_LEN body
	// bounds, not a claim about every agent implementation's limits.
	agentFWMaxBody    = 256 << 10
	agentFWBufferSize = 32 << 10

	agentFWFailure           = 5
	agentFWSuccess           = 6
	agentFWRequestIdentities = 11
	agentFWIdentities        = 12
	agentFWSign              = 13
	agentFWSignature         = 14
	agentFWExtension         = 27
	agentFWExtensionFailure  = 28
	agentFWSessionBind       = "session-bind@openssh.com"
)

type agentFirewall struct {
	remote net.Conn
	source net.Conn
	buffer []byte
}

// forwardAgentAuthentication owns these two connection handles. It never
// opens an unbound fallback: each fresh source must accept the verified
// outer forwarding prefix before any remote request can reach it.
func forwardAgentAuthentication(ctx context.Context, remote, source net.Conn, binding *peerAgentBinding) (err error) {
	var stop func() bool
	var stopped chan struct{}
	defer func() {
		err = finishAgentFirewall(ctx, remote, source, err, stop, stopped)
	}()
	if err := checkAgentFirewallBinding(binding); err != nil {
		return err
	}
	if remote == nil || source == nil {
		return newAgentSourceFailure("agent connection endpoints are unavailable", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stopped = make(chan struct{})
	stop = context.AfterFunc(ctx, func() {
		defer close(stopped)
		interruptAgentFirewall(remote, source)
	})
	f := agentFirewall{remote: remote, source: source, buffer: make([]byte, agentFWBufferSize)}
	if err := f.bindSource(binding.wire); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		head, length, opcode, err := readAgentFirewallHead(remote)
		if err == io.EOF {
			return nil // A clean EOF is possible only between requests.
		}
		if err != nil {
			return fmt.Errorf("read remote agent request: %w", err)
		}
		if err := f.request(head, length, opcode); err != nil {
			return err
		}
	}
}

func finishAgentFirewall(ctx context.Context, remote, source net.Conn, err error, stop func() bool, stopped <-chan struct{}) error {
	if err != nil {
		interruptAgentFirewall(remote, source)
	} else {
		// Revoke the source before a graceful stream close can wait for
		// its final queued response. Cancellation remains armed here.
		if source != nil {
			_ = source.Close()
		}
		if remote != nil {
			if closeErr := remote.Close(); closeErr != nil {
				err = fmt.Errorf("close remote agent stream: %w", closeErr)
			}
		}
	}
	if stop != nil && !stop() {
		<-stopped
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func interruptAgentFirewall(remote, source net.Conn) {
	// Set both deadlines before either Close can wait on a stream.
	now := time.Now()
	if remote != nil {
		_ = remote.SetDeadline(now)
	}
	if source != nil {
		_ = source.SetDeadline(now)
	}
	if source != nil {
		_ = source.Close()
	}
	if remote != nil {
		_ = remote.Close()
	}
}

func (f *agentFirewall) request(head [5]byte, length uint32, opcode byte) error {
	switch opcode {
	case agentFWRequestIdentities:
		if length != 1 {
			return f.deny(length-1, agentFWFailure)
		}
		return f.forward(head[:], 0, opcode)
	case agentFWSign:
		return f.forward(head[:], length-1, opcode)
	case agentFWExtension:
		return f.extension(head, length-1)
	default:
		return f.deny(length-1, agentFWFailure)
	}
}

func (f *agentFirewall) extension(head [5]byte, remaining uint32) error {
	if remaining < 4 {
		return f.deny(remaining, agentFWExtensionFailure)
	}
	var nameLength [4]byte
	if _, err := io.ReadFull(f.remote, nameLength[:]); err != nil {
		return fmt.Errorf("read remote agent extension name: %w", err)
	}
	remaining -= 4
	n := agentFirewallUint32(nameLength[:])
	if n != uint32(len(agentFWSessionBind)) || n > remaining {
		return f.deny(remaining, agentFWExtensionFailure)
	}
	var prefix [5 + 4 + len(agentFWSessionBind)]byte
	copy(prefix[:5], head[:])
	copy(prefix[5:9], nameLength[:])
	if _, err := io.ReadFull(f.remote, prefix[9:]); err != nil {
		return fmt.Errorf("read remote agent extension name: %w", err)
	}
	remaining -= n
	if string(prefix[9:]) != agentFWSessionBind {
		return f.deny(remaining, agentFWExtensionFailure)
	}
	// No byte of an extension's header or name reaches the source until
	// its exact allowed name has been checked in full.
	return f.forward(prefix[:], remaining, agentFWExtension)
}

func (f *agentFirewall) deny(remaining uint32, opcode byte) error {
	if err := drainAgentFirewallBody(f.remote, remaining, f.buffer); err != nil {
		return fmt.Errorf("drain denied remote agent request: %w", err)
	}
	response := [5]byte{0, 0, 0, 1, opcode}
	if err := writeAgentFirewallBytes(f.remote, response[:]); err != nil {
		return fmt.Errorf("write denied agent response: %w", err)
	}
	return nil
}

func (f *agentFirewall) forward(prefix []byte, remaining uint32, opcode byte) error {
	if err := writeAgentFirewallBytes(f.source, prefix); err != nil {
		return newAgentSourceFailure("write agent request", err)
	}
	readErr, writeErr := copyAgentFirewallBody(f.source, f.remote, remaining, f.buffer)
	if readErr != nil {
		return fmt.Errorf("read remote agent request body: %w", readErr)
	}
	if writeErr != nil {
		return newAgentSourceFailure("write agent request body", writeErr)
	}
	return f.reply(opcode)
}

func (f *agentFirewall) reply(request byte) error {
	head, length, opcode, err := readAgentFirewallHead(f.source)
	if err != nil {
		return newAgentSourceFailure("read agent response", err)
	}
	if !allowedAgentFirewallReply(request, opcode, length) {
		return newAgentSourceFailure("invalid agent response", nil)
	}
	if err := writeAgentFirewallBytes(f.remote, head[:]); err != nil {
		return fmt.Errorf("write remote agent response: %w", err)
	}
	readErr, writeErr := copyAgentFirewallBody(f.remote, f.source, length-1, f.buffer)
	if readErr != nil {
		return newAgentSourceFailure("read agent response body", readErr)
	}
	if writeErr != nil {
		return fmt.Errorf("write remote agent response body: %w", writeErr)
	}
	return nil
}

func allowedAgentFirewallReply(request, opcode byte, length uint32) bool {
	if opcode == agentFWFailure {
		return length == 1
	}
	switch request {
	case agentFWRequestIdentities:
		return opcode == agentFWIdentities && length >= 5
	case agentFWSign:
		return opcode == agentFWSignature && length >= 5
	case agentFWExtension:
		return (opcode == agentFWSuccess || opcode == agentFWExtensionFailure) && length == 1
	}
	return false
}
