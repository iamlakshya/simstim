package client

import (
	"context"
	"strings"
)

// commandArgs enables only the private binding collector. Its endpoint
// cannot list or sign and is never the source agent's socket.
func (c *agentBindingCollector) commandArgs(ctx context.Context, args []string) ([]string, error) {
	if c == nil || c.socket == nil || strings.ContainsAny(c.socket.path, "\x00\r\n\"%$") {
		return nil, agentBindingProofError()
	}
	out := append([]string(nil), args...)
	count := 0
	for i := 0; i < len(out); i++ {
		if out[i] == "-A" || out[i] == "-a" {
			return nil, agentBindingProofError()
		}
		if out[i] == "-o" && i+1 < len(out) {
			key, _, _ := strings.Cut(out[i+1], "=")
			if !strings.EqualFold(strings.TrimSpace(key), "ForwardAgent") {
				continue
			}
			if out[i+1] != "ForwardAgent=no" {
				return nil, agentBindingProofError()
			}
			out[i+1] = "ForwardAgent=" + c.socket.path
			count++
		}
	}
	if count != 1 {
		return nil, agentBindingProofError()
	}
	// -vv is a native flag, so it sets the destination startup diagnostic
	// level even after the pin's scalar LogLevel=ERROR. It changes no auth
	// environment or jump-host configuration, and -E stays target-only.
	out = append(out, "-vv")
	values, _, err := sshG(ctx, out, c.ownerTarget())
	if err != nil || len(values) == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, agentBindingProofError()
	}
	return out, nil
}

func (c *agentBindingCollector) ownerTarget() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}
