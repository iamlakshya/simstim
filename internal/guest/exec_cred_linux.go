//go:build linux

package guest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func setAgentExecCredential(cmd *exec.Cmd, groups []uint32) {
	if os.Geteuid() != 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    GuestAgentUID,
		Gid:    GuestAgentGID,
		Groups: groups,
	}
}

// chownPathForAgent recursively chowns created files under rootDir to
// GuestAgentUID:GuestAgentGID when running as root inside a microvm before the
// agent starts.
func chownPathForAgent(rootDir string) {
	if os.Geteuid() != 0 || rootDir == "" {
		return
	}
	_ = filepath.WalkDir(rootDir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		//nolint:gosec // G122: chowning guest tmpfs /home/agent as PID 1 before agent starts
		_ = os.Lchown(p, GuestAgentUID, GuestAgentGID)
		return nil
	})
}

func chownMicroVMDir(dir string, uid, gid int, mode os.FileMode) error {
	//nolint:gosec // G301: caller explicitly supplies required guest directory mode (0700 or 0750)
	if err := os.MkdirAll(dir, mode); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	//nolint:gosec // G703: dir is constructed by wall-init under guest /run
	if err := os.Chmod(dir, mode); err != nil {
		return fmt.Errorf("chmod dir %s: %w", dir, err)
	}
	if os.Geteuid() == 0 {
		//nolint:gosec // G703: dir is constructed by wall-init under guest /run
		_ = os.Chown(dir, uid, gid)
	}
	return nil
}

func chownFileForAgent(path string) {
	if os.Geteuid() == 0 {
		_ = os.Chown(path, GuestAgentUID, GuestAgentGID)
	}
}

func killAgentGroup(pid int) {
	if pid > 1 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
