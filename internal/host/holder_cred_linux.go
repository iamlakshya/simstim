//go:build linux

package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const ttyGroupID = 5

// startAgentPTY starts cmd attached to a new PTY. When g specifies an AgentUID
// (microvm UID separation, spec 17.10), the slave PTY is chowned to the agent
// UID and cmd starts directly as that UID/GID with its supplementary groups.
func startAgentPTY(cmd *exec.Cmd, ws *pty.Winsize, g *guestConfig) (*os.File, error) {
	if g == nil || g.agentUID <= 0 {
		ptmx, err := pty.StartWithSize(cmd, ws)
		if err != nil {
			return nil, fmt.Errorf("start pty: %w", err)
		}
		return ptmx, nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Credential: &syscall.Credential{
			Uid:    uint32(g.agentUID), //nolint:gosec // G115: validated positive Linux UID
			Gid:    uint32(g.agentGID), //nolint:gosec // G115: validated positive Linux GID
			Groups: g.agentGroups,
		},
	}
	return startPTYWithCredential(cmd, ws)
}

// startSSHExecPTY starts cmd in a PTY while preserving any pre-configured
// SysProcAttr.Credential (for microvm guest exec running as user agent).
func startSSHExecPTY(cmd *exec.Cmd, ws *pty.Winsize) (*os.File, error) {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		ptmx, err := pty.StartWithSize(cmd, ws)
		if err != nil {
			return nil, fmt.Errorf("start ssh-exec PTY: %w", err)
		}
		return ptmx, nil
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	return startPTYWithCredential(cmd, ws)
}

func startPTYWithCredential(cmd *exec.Cmd, ws *pty.Winsize) (*os.File, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}
	if ws != nil {
		if err := pty.Setsize(ptmx, ws); err != nil {
			_ = tty.Close()
			_ = ptmx.Close()
			return nil, fmt.Errorf("set pty size: %w", err)
		}
	}
	uid := int(cmd.SysProcAttr.Credential.Uid)
	if err := os.Chown(tty.Name(), uid, ttyGroupID); err != nil {
		_ = tty.Close()
		_ = ptmx.Close()
		return nil, fmt.Errorf("chown pty %s to uid %d: %w", tty.Name(), uid, err)
	}
	//nolint:gosec // G302: slave PTY uses standard 0620 (uid:tty) permissions
	if err := os.Chmod(tty.Name(), 0o620); err != nil {
		_ = tty.Close()
		_ = ptmx.Close()
		return nil, fmt.Errorf("chmod pty %s: %w", tty.Name(), err)
	}
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	if err := cmd.Start(); err != nil {
		_ = tty.Close()
		_ = ptmx.Close()
		return nil, fmt.Errorf("start command on pty: %w", err)
	}
	_ = tty.Close()
	return ptmx, nil
}

// dropHolderPrivileges permanently drops the holder process from root to the
// dedicated guest holder UID/GID (900:900), clears dumpable and sets
// NoNewPrivs so the agent (uid 1000) cannot ptrace or inspect the holder
// without sudo (isolation spec 17.10).
func dropHolderPrivileges(g *guestConfig) error {
	if g == nil || g.holderUID <= 0 {
		return nil
	}
	if err := unix.Setgroups([]int{g.holderGID}); err != nil {
		return fmt.Errorf("drop holder groups: %w", err)
	}
	if err := unix.Setresgid(g.holderGID, g.holderGID, g.holderGID); err != nil {
		return fmt.Errorf("drop holder gid %d: %w", g.holderGID, err)
	}
	if err := unix.Setresuid(g.holderUID, g.holderUID, g.holderUID); err != nil {
		return fmt.Errorf("drop holder uid %d: %w", g.holderUID, err)
	}
	_ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
	return setHolderNoNewPrivs()
}

// setHolderNoNewPrivs sets PR_SET_NO_NEW_PRIVS on every OS thread in the
// process via AllThreadsSyscall6 (supported in all CGO_ENABLED=0 host helper
// binaries). Under cgo builds (such as `go test -race`), Go's runtime returns
// ENOTSUP from AllThreadsSyscall6, so fall back to unix.Prctl on the caller.
func setHolderNoNewPrivs() error {
	if _, _, errno := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); errno != 0 {
		if errno == unix.ENOTSUP {
			if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
				return fmt.Errorf("set holder no_new_privs: %w", err)
			}
			return nil
		}
		return fmt.Errorf("set holder no_new_privs: %w", errno)
	}
	return nil
}

// placeGuestAgentInCgroup moves the guest agent process into the agent memory
// cgroup and raises its oom_score_adj while the holder is still root, before
// dropHolderPrivileges runs (isolation spec 20.1).
func placeGuestAgentInCgroup(pid int, g *guestConfig) {
	if g == nil || pid <= 1 {
		return
	}
	if g.agentCgroup != "" {
		//nolint:gosec // G306: cgroup control file
		_ = os.WriteFile(filepath.Join(g.agentCgroup, "cgroup.procs"), []byte(strconv.Itoa(pid)+"\n"), 0o644)
	}
	if g.agentUID > 0 || g.agentCgroup != "" {
		//nolint:gosec // G306: procfs kernel control file
		_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", pid), []byte("500"), 0o644)
	}
}

func chownFileForHolder(path string, g *guestConfig) {
	if g == nil || g.holderUID <= 0 {
		return
	}
	_ = os.Chown(path, g.holderUID, g.holderGID)
}

func ensureHolderSocketPerms(sock, hook string, g *guestConfig) error {
	if g == nil || g.holderUID <= 0 {
		return nil
	}
	if err := os.Chown(sock, g.holderUID, g.holderGID); err != nil {
		return fmt.Errorf("chown holder socket: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return fmt.Errorf("chmod holder socket: %w", err)
	}
	hookGID := g.hookGID
	if hookGID <= 0 {
		hookGID = g.agentGID
	}
	if err := os.Chown(hook, g.holderUID, hookGID); err != nil {
		return fmt.Errorf("chown hook socket: %w", err)
	}
	//nolint:gosec // G302: hook socket must be 0660 (holder:agent) so agent hooks can connect
	if err := os.Chmod(hook, 0o660); err != nil {
		return fmt.Errorf("chmod hook socket: %w", err)
	}
	return nil
}
