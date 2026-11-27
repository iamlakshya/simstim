package client

import (
	"context"
	"net"
	"os"
	"runtime"
	"time"
)

// CheckLocalSSHAgent checks that the laptop has an agent to forward.
// Forwarding also needs a platform gate that can revoke its open streams.
func CheckLocalSSHAgent() error {
	return CheckLocalSSHAgentContext(context.Background())
}

// CheckLocalSSHAgentContext checks the local agent without losing a
// command's cancellation while it connects to the socket.
func CheckLocalSSHAgentContext(ctx context.Context) error {
	return checkLocalSSHAgentHealth(ctx, os.Getenv("SSH_AUTH_SOCK"))
}

func checkLocalSSHAgentHealth(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkLocalSSHAgentPath(path); err != nil {
		return err
	}
	d := net.Dialer{Timeout: time.Second}
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return missingLocalAgent()
	}
	if err := c.Close(); err != nil {
		return missingLocalAgent()
	}
	return nil
}

func missingLocalAgent() error {
	return &UserError{Msg: "there is no reachable SSH agent on this computer",
		Hint: "Start an SSH agent and add the key before trying again.", Code: "ssh_agent_missing"}
}

func unsupportedLocalAgent() error {
	return &UserError{Msg: "forwarding this computer's SSH agent needs a local socket gate that is unavailable on Windows",
		Hint: "Attach from macOS or Linux, or use a separate deploy key on the host.", Code: "ssh_agent_unsupported", Kind: KindRefused}
}

// checkLocalSSHAgentPath checks only local metadata. Publication rechecks
// it without opening another connection under the host operation lock.
func checkLocalSSHAgentPath(path string) error {
	if runtime.GOOS == "windows" {
		return unsupportedLocalAgent()
	}
	st, err := os.Stat(path)
	if path == "" || err != nil || st.Mode()&os.ModeSocket == 0 {
		return missingLocalAgent()
	}
	return nil
}
