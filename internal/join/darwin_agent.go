//go:build darwin

package join

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func macAgentDirectory(owner *macAccount) (*os.Root, *os.File, error) {
	info, err := os.Lstat(owner.Home)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(info.Sys().(*syscall.Stat_t).Uid) != owner.UID {
		return nil, nil, macFailure("join_mac_agent", "the original Mac user home changed")
	}
	if err := macMakeUserParents(filepath.Join(owner.Home, "Library/LaunchAgents"), owner); err != nil {
		return nil, nil, err
	}
	home, err := os.OpenRoot(owner.Home)
	if err != nil {
		return nil, nil, err
	}
	agents, err := home.OpenRoot("Library/LaunchAgents")
	home.Close()
	if err != nil {
		return nil, nil, err
	}
	file, err := agents.Open(".")
	if err != nil {
		agents.Close()
		return nil, nil, err
	}
	return agents, file, nil
}

func (e *macExecutor) installMacAgentLink(ctx context.Context, owner *macAccount, target, label string) error {
	current, err := macReadAccount(ctx, owner.Name)
	if err != nil || current.UID != owner.UID || current.GUID != owner.GUID || current.Home != owner.Home {
		return macFailure("join_mac_agent", "the original Mac launch-agent user changed")
	}
	root, dir, err := macAgentDirectory(owner)
	if err != nil {
		return err
	}
	defer root.Close()
	defer dir.Close()
	name := label + ".plist"
	step, err := e.intent("agent-link", filepath.Join(owner.Home, "Library/LaunchAgents", name))
	if err != nil {
		return err
	}
	var native unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &native, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if native.Mode&unix.S_IFMT != unix.S_IFLNK || uint64(native.Dev) != step.After.Device || native.Ino != step.After.Inode {
			return macFailure("join_mac_agent", "the original launch-agent link was replaced or already exists")
		}
		return nil
	} else if err != unix.ENOENT {
		return err
	}
	anchor := filepath.Join(e.dir, "anchors", "agent-link-after")
	if _, err := os.Lstat(anchor); errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(target, anchor); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	info, err := os.Lstat(anchor)
	if err != nil {
		return err
	}
	original := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink == 0 {
		return macFailure("join_mac_agent", "the original agent-link anchor has another type")
	}
	step.After = macFileState{Exists: true, Device: uint64(original.Dev), Inode: original.Ino, Mode: uint32(info.Mode())}
	if err := e.save(); err != nil {
		return err
	}
	if err := unix.Linkat(unix.AT_FDCWD, anchor, int(dir.Fd()), name, 0); err != nil {
		return fmt.Errorf("publish the original launch-agent link: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return err
	}
	return e.applied("agent-link")
}

func (e *macExecutor) undoMacAgentLink(ctx context.Context, step *macStep) error {
	owner, err := macReadAccount(ctx, e.ledger.AgentUser)
	if err != nil || owner.UID != e.ledger.AgentUID || owner.GUID != e.ledger.AgentGUID || owner.Home != e.ledger.AgentHome {
		return macFailure("join_mac_agent", "the original agent user changed before leave")
	}
	root, dir, err := macAgentDirectory(owner)
	if err != nil {
		return err
	}
	defer root.Close()
	defer dir.Close()
	name := "io.lima-vm.autostart." + e.ledger.Instance + ".plist"
	var native unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &native, unix.AT_SYMLINK_NOFOLLOW); err == unix.ENOENT {
		return nil
	} else if err != nil {
		return err
	}
	if native.Mode&unix.S_IFMT != unix.S_IFLNK || uint64(native.Dev) != step.After.Device || native.Ino != step.After.Inode {
		return macFailure("join_mac_agent", "a changed agent link was retained")
	}
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		return err
	}
	return dir.Sync()
}
