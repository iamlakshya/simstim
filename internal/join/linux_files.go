//go:build linux

package join

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
)

func (e *linuxExecutor) applyFile(ctx context.Context, id, path string, data []byte, mode os.FileMode, uid, gid int) (*ledgerEntry, error) {
	current, err := snapshotFile(path)
	if err != nil {
		return nil, err
	}
	entry := e.ledger.entry(id)
	if entry == nil {
		if sameFileState(current, fileState{true, data, uint32(mode), uid, gid}) {
			return nil, nil
		}
		entry, err = e.ahead(ledgerEntry{ID: id, Kind: "file", Path: path, Before: current, After: fileState{true, data, uint32(mode), uid, gid}})
		if err != nil {
			return nil, err
		}
	}
	if entry.Path != path {
		return nil, codeFailure("join_changed", "a join file's target changed")
	}
	if sameFileState(current, entry.After) {
		return entry, e.applied(entry)
	}
	if !sameFileState(current, entry.Before) {
		return nil, codeFailure("join_changed", "a file changed while the join was incomplete")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = durableFile(path, entry.After.Data, os.FileMode(entry.After.Mode), entry.After.UID, entry.After.GID); err != nil {
		return nil, err
	}
	return entry, e.applied(entry)
}
func (e *linuxExecutor) authoriseKey(ctx context.Context, u *user.User, key string) error {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid <= 0 {
		return codeFailure("join_account", "the account uid is invalid")
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil || gid < 0 {
		return codeFailure("join_account", "the account gid is invalid")
	}
	// OpenRoot confines every relative operation to the checked home even if
	// the account concurrently changes a name within it.
	if err = safeHome(u.HomeDir, uid); err != nil {
		return err
	}
	root, err := os.OpenRoot(u.HomeDir)
	if err != nil {
		return err
	}
	defer root.Close()
	st, err := root.Lstat(".ssh")
	entry := e.ledger.entry("ssh-directory")
	if errors.Is(err, os.ErrNotExist) {
		if entry != nil && entry.State == "applied" {
			return codeFailure("join_changed", "the joined SSH directory was removed")
		}
		if entry == nil {
			entry, err = e.ahead(ledgerEntry{ID: "ssh-directory", Kind: "directory", Path: filepath.Join(u.HomeDir, ".ssh"), Home: u.HomeDir, UID: uid, GID: gid})
			if err != nil {
				return err
			}
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = root.Mkdir(".ssh", 0o700); err != nil {
			return err
		}
		st, err = root.Lstat(".ssh")
	}
	if err != nil {
		return err
	}
	owner, _ := fileOwner(st)
	if entry != nil {
		dev, ino, err := identity(entry.Path)
		if err != nil {
			return err
		}
		if entry.Inode == 0 {
			// A crash before recording creation can resume only while the original
			// empty root-owned directory has not been handed to the account.
			if owner != 0 || !st.IsDir() || st.Mode().Perm() != 0o700 {
				return codeFailure("join_changed", "an incomplete SSH directory has uncertain creation ownership")
			}
			entry.Device, entry.Inode = dev, ino
			if err = e.save(); err != nil {
				return err
			}
		} else if entry.Device != dev || entry.Inode != ino {
			return codeFailure("join_changed", "the original SSH directory was replaced")
		}
		if owner == 0 && entry.State == "prepared" {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = root.Chown(".ssh", uid, gid); err != nil {
				return err
			}
			st, err = root.Lstat(".ssh")
			if err != nil {
				return err
			}
			owner, _ = fileOwner(st)
		}
		if err = e.applied(entry); err != nil {
			return err
		}
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || owner != uid || st.Mode().Perm()&0o022 != 0 {
		return codeFailure("join_key", "the account's .ssh directory has unsafe ownership or permissions")
	}
	return e.applyKey(ctx, u.HomeDir, uid, gid, "restrict,pty,port-forwarding "+key)
}
func (e *linuxExecutor) configureSSHD(ctx context.Context) error {
	b, err := os.ReadFile("/etc/ssh/sshd_config")
	if err != nil {
		return err
	}
	if !HasSSHDInclude(string(b)) {
		return missingSSHDInclude()
	}
	dir := "/etc/ssh/sshd_config.d"
	if err = protectedPath(dir, true); err != nil {
		return err
	}
	text, err := SSHDDropIn(e.account)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "00-"+brand.Name+".conf")
	before, err := snapshotFile(path)
	if err != nil {
		return err
	}
	// Preserve earlier accounts' Match blocks. Replacing the shared drop-in
	// could re-enable remote forwarding for a previously joined account.
	if before.Exists && string(before.Data) != text {
		prefix := string(before.Data)
		if !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		text = prefix + "Match all\n" + text
	}

	entry, err := e.applyFile(ctx, "sshd", path, []byte(text), 0o644, 0, 0)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		if entry == nil {
			return cause
		}
		undoErr := restoreFile(entry)
		if undoErr == nil {
			entry.State = "undone"
			undoErr = e.save()
		}
		return errors.Join(cause, undoErr)
	}
	if _, err = command(ctx, nil, "/usr/sbin/sshd", "-t"); err != nil {
		return rollback(codeFailure("join_sshd", "sshd rejected the new configuration; its old drop-in was restored"))
	}
	effective, err := command(ctx, nil, "/usr/sbin/sshd", "-T", "-C", "user="+e.account)
	if err != nil {
		return rollback(err)
	}
	if err = VerifySSHD(string(effective)); err != nil {
		return rollback(err)
	}
	if err = reloadSSHD(ctx); err != nil {
		original := err
		var restoreErr, saveErr error
		if entry != nil {
			restoreErr = restoreFile(entry)
			entry.State = "prepared"
			saveErr = e.save()
		}
		if restoreErr != nil {
			return errors.Join(original, restoreErr, saveErr)
		}
		// File restoration alone cannot confirm rollback of a live daemon.
		// No foreign file is reloaded after a refused restoration.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		_, checkErr := command(recovery, nil, "/usr/sbin/sshd", "-t")
		var reloadErr error
		if checkErr == nil {
			reloadErr = reloadSSHD(recovery)
		}
		if entry != nil && checkErr == nil && reloadErr == nil {
			entry.State = "undone"
			saveErr = errors.Join(saveErr, e.save())
		}
		return errors.Join(original, saveErr, checkErr, reloadErr)
	}

	return nil
}

func missingSSHDInclude() error {
	return &code.Error{Code: "join_sshd", Message: "sshd_config does not include its drop-in folder", Hint: "Add Include /etc/ssh/sshd_config.d/*.conf before every Match block in /etc/ssh/sshd_config, then retry host join."}
}
func reloadSSHD(ctx context.Context) error {
	// An active socket may feed a persistent ssh.service (Accept=no). Reload
	// that daemon first; socket presence alone does not prove per-connection reads.
	for _, unit := range []string{"ssh.service", "sshd.service"} {
		status, err := command(ctx, nil, "/usr/bin/systemctl", "show", unit, "--property=ActiveState", "--value")
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(status)) {
		case "active", "reloading":
			_, err = command(ctx, nil, "/usr/bin/systemctl", "reload", unit)
			return err
		case "inactive", "failed", "":
		case "activating", "deactivating":
			return codeFailure("join_sshd", "the SSH service is changing state; retry after it settles")
		default:
			return codeFailure("join_sshd", "the SSH service has an unknown active state")
		}
	}
	for _, unit := range []string{"ssh.socket", "sshd.socket"} {
		if _, err := command(ctx, nil, "/usr/bin/systemctl", "is-active", "--quiet", unit); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("reload SSH: %w", codeFailure("join_sshd", "no active SSH service or socket was found"))
}
