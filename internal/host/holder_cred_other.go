//go:build !linux

package host

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startAgentPTY(cmd *exec.Cmd, ws *pty.Winsize, _ *guestConfig) (*os.File, error) {
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}
	return ptmx, nil
}

func startSSHExecPTY(cmd *exec.Cmd, ws *pty.Winsize) (*os.File, error) {
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return nil, fmt.Errorf("start ssh-exec PTY: %w", err)
	}
	return ptmx, nil
}

func dropHolderPrivileges(*guestConfig) error                    { return nil }
func placeGuestAgentInCgroup(int, *guestConfig)                  {}
func chownFileForHolder(string, *guestConfig)                    {}
func ensureHolderSocketPerms(string, string, *guestConfig) error { return nil }
