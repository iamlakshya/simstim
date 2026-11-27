package host

import "os/exec"

// startSessionCommand linearises a sandbox's actual launch against the
// bridge's existing stop registry. It never holds the mutex while waiting
// for systemd, the supervisor's socket or the agent. Once Start succeeds,
// the manifest belongs to an owned session even if its reply is lost.
func startSessionCommand(m *Manifest, cmd *exec.Cmd) error {
	if m.Files == nil {
		return cmd.Start()
	}
	liveGitOpsMu.Lock()
	defer liveGitOpsMu.Unlock()
	if gitOpsStopped {
		return UserError("git_op", "the connection ended before the sandbox launched", "Start the session again.")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.launchStarted = true
	return nil
}
