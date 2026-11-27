package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// RunGitFetch fetches through one validated SSH snapshot. The private
// executor records SSH's own exit state, which Git's exit128 otherwise hides.
func RunGitFetch(ctx context.Context, h Host, root, branch, into string, force bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logFile, err := newSSHLog()
	if err != nil {
		return nil, err
	}
	defer logFile.finish()
	sshArgs, err := (&SSH{Host: h}).connArgs(ctx, true)
	if err != nil {
		return nil, err
	}
	sshArgs = append(sshArgs, NoPortForwards()...)
	sshArgs = append(sshArgs, "-E", logFile.path)
	entryDigest, err := gitSSHEntryDigest(h)
	if err != nil {
		return nil, err
	}
	config, err := json.Marshal(gitSSHConfig{Version: 2, Name: h.Name, Args: sshArgs, Target: h.SSH, Root: root, EntryDigest: entryDigest})
	if err != nil {
		return nil, fmt.Errorf("encode the Git SSH configuration: %w", err)
	}
	sum := sha256.Sum256(config)
	digest := hex.EncodeToString(sum[:])
	configPath := filepath.Join(logFile.dir, "config.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return nil, fmt.Errorf("write the Git SSH configuration: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the Git SSH executor: %w", err)
	}
	if runtime.GOOS == "windows" {
		executable = filepath.ToSlash(executable)
	}
	lease, err := startGitLease(logFile.dir)
	if err != nil {
		return nil, err
	}
	defer lease.close()
	command := exec.CommandContext(ctx, "git", FetchArgs(h, root, branch, into, force)...)
	command.WaitDelay = sshWaitDelay
	command.Cancel = func() error { lease.close(); return terminateGitProcess(command.Process) }
	command.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=ssh", "GIT_SSH_VARIANT=ssh", "GIT_SSH_COMMAND="+shellWord(executable)+" "+gitSSHMode+" "+shellWord(configPath)+" "+digest)
	output, runErr := command.CombinedOutput()
	if !pinned(h.Name) {
		cleanupUnpinnedMaster(ctx, h, sshArgs)
	}
	lease.close()
	if err := ctx.Err(); err != nil {
		return output, err
	}
	if runErr == nil {
		return output, nil
	}
	status, ok := readGitSSHStatus(filepath.Join(logFile.dir, "status.json"))
	if ok && status.LocalError != nil {
		return output, status.LocalError
	}
	if !ok || status.Stopped || !status.Exited {
		return output, fmt.Errorf("fetch the branch: %w", runErr)
	}
	log, err := os.ReadFile(logFile.path)
	if err != nil {
		return output, fmt.Errorf("fetch the branch: %w", runErr)
	}
	if status.Code == 255 {
		if refusal := hostKeyError(h, string(log), status.Code); refusal != nil {
			return output, refusal
		}
	}
	if detail := tailStderr(string(log)); detail != "" {
		return output, fmt.Errorf("%w: %s", runErr, detail)
	}
	return output, fmt.Errorf("fetch the branch: %w", runErr)
}
