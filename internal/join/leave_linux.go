//go:build linux

package join

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// LeaveLinux undoes this account's original join entries, newest first. It
// refuses writable ledgers, changed files, changed accounts and root journals
// whose native identities no longer match. A failed undo retains its ledger.
func LeaveLinux(ctx context.Context, account string, out io.Writer) error {
	e, unlock, err := openLinuxExecutor(account, out, false)
	if err != nil {
		return err
	}
	defer unlock()
	l, err := readLedger(filepath.Join(e.dir, "ledger.json"))
	if err != nil {
		return err
	}
	if l.Account != account {
		return codeFailure("join_ledger", "the leave ledger belongs to another account")
	}
	e.ledger = l
	if err = protectedPath(filepath.Join(e.dir, "leave.sh"), false); err != nil {
		return err
	}
	for n := len(l.Entries) - 1; n >= 0; n-- {
		entry := &l.Entries[n]
		if entry.State == "undone" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Undo %s\n", entry.ID)
		if err = e.undo(ctx, entry); err != nil {
			return err
		}
		entry.State = "undone"
		if err = e.save(); err != nil {
			return err
		}
	}
	// Remove only fixed leaves after every actual undo has completed. Shared
	// binaries stay available for other accounts' original leave scripts.
	for _, name := range []string{"leave.sh", "ledger.json"} {
		if err = os.Remove(filepath.Join(e.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err = os.Remove(e.dir); err != nil {
		return err
	}
	return nil
}
func (e *linuxExecutor) undo(ctx context.Context, entry *ledgerEntry) error {
	switch entry.Kind {
	case "file":
		if entry.ID == "key" {
			return restoreKey(entry)
		}
		expected := filepath.Join("/etc/ssh/sshd_config.d", "00-"+brand.Name+".conf")
		if entry.ID != "sshd" || entry.Path != expected {
			return codeFailure("join_ledger", "the leave ledger has an unknown file role")
		}
		if err := protectedPath(filepath.Dir(expected), true); err != nil {
			return err
		}
		if err := restoreFile(entry); err != nil {
			return err
		}
		if _, err := command(ctx, nil, "/usr/sbin/sshd", "-t"); err != nil {
			return err
		}
		return reloadSSHD(ctx)
	case "directory":
		if entry.ID != "ssh-directory" || entry.Path != filepath.Join(entry.Home, ".ssh") {
			return codeFailure("join_ledger", "the leave ledger has an unknown directory role")
		}
		if err := safeHome(entry.Home, entry.UID); err != nil {
			return err
		}
		root, err := os.OpenRoot(entry.Home)
		if err != nil {
			return err
		}
		defer root.Close()
		st, err := root.Lstat(".ssh")
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		owner, _ := fileOwner(st)
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || owner != entry.UID {
			return codeFailure("join_changed", "the original SSH directory changed")
		}
		dev, ino, err := identity(entry.Path)
		if err != nil {
			return err
		}
		if entry.Inode == 0 || entry.Device != dev || entry.Inode != ino {
			return codeFailure("join_changed", "the original SSH directory was replaced")
		}
		return root.Remove(".ssh")
	case "group":
		if entry.ID != "group" || entry.GID <= 0 {
			return codeFailure("join_ledger", "the leave ledger has an invalid private group role")
		}
		group, err := user.LookupGroup(e.account)
		if _, unknown := err.(user.UnknownGroupError); unknown {
			return nil
		}
		if err != nil {
			return err
		}
		if group.Gid != strconv.Itoa(entry.GID) {
			return codeFailure("join_changed", "the original private group changed; the undo left it in place")
		}
		members, err := command(ctx, nil, "/usr/bin/getent", "group", e.account)
		if err != nil {
			return err
		}
		fields := strings.SplitN(strings.TrimSpace(string(members)), ":", 4)
		if len(fields) != 4 || fields[0] != e.account || fields[2] != strconv.Itoa(entry.GID) || fields[3] != "" {
			return codeFailure("join_changed", "the original private group gained members; the undo left it in place")
		}
		_, err = command(ctx, nil, "/usr/sbin/groupdel", e.account)
		return err
	case "account":
		if entry.ID != "account" || entry.Home != filepath.Join("/home", e.account) || entry.UID <= 0 {
			return codeFailure("join_ledger", "the leave ledger has an invalid account role")
		}
		if _, err := user.Lookup(e.account); err != nil {
			if _, unknown := err.(user.UnknownUserError); unknown {
				if _, homeErr := os.Lstat(entry.Home); errors.Is(homeErr, os.ErrNotExist) {
					return nil
				}
				return codeFailure("join_changed", "the created account disappeared but its home remains; preserve it for the owner's inspection")
			}
			return err
		}
		if _, err := e.verifyCreatedAccount(entry, false); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Leave is the explicit local request to stop ALL processes of only this
		// original join-created account. Existing/replaced accounts never reach it.
		if err := refuseOwnCreatedAccountSession(entry); err != nil {
			return err
		}
		if _, err := e.verifyCreatedAccount(entry, false); err != nil {
			return err
		}
		_, stopErr := command(ctx, nil, "/usr/bin/loginctl", "terminate-user", strconv.Itoa(entry.UID))
		if err := e.waitCreatedAccountStopped(ctx, entry); err != nil {
			return errors.Join(stopErr, err)
		}
		if _, err := e.verifyCreatedAccount(entry, false); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := command(ctx, nil, "/usr/sbin/userdel", "--remove", e.account)
		return err
	case "root":
		if entry.ID != "linger" && entry.ID != "sandbox" {
			return codeFailure("join_ledger", "the leave ledger has an unknown root role")
		}
		if err := e.captureJournals(entry); err != nil {
			return err
		}
		existing := false
		for _, j := range entry.Journals {
			if _, err := os.Lstat(filepath.Join(rootAccountDir(e.account), "journal", j.Name)); err == nil {
				existing = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if !existing {
			return nil
		}
		if err := e.verifyPresentJournals(entry); err != nil {
			return err
		}
		b, err := command(ctx, strings.NewReader(entry.Undo), "/bin/sh")
		if len(b) > 0 {
			_, _ = e.out.Write(b)
		}
		if err != nil {
			return err
		}
		for _, j := range entry.Journals {
			if _, err = os.Lstat(filepath.Join(rootAccountDir(e.account), "journal", j.Name)); !errors.Is(err, os.ErrNotExist) {
				return codeFailure("join_undo", "a root-step undo retained its original journal; keep the leave script")
			}
		}
		return nil
	default:
		return codeFailure("join_ledger", "the leave ledger has an unknown undo role")
	}
}
func (e *linuxExecutor) verifyPresentJournals(entry *ledgerEntry) error {
	for _, j := range entry.Journals {
		path := filepath.Join(rootAccountDir(e.account), "journal", j.Name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := protectedPath(path, true); err != nil {
			return err
		}
		dev, ino, err := identity(path)
		if err != nil {
			return err
		}
		if j.Inode == 0 || j.Device != dev || j.Inode != ino {
			return codeFailure("join_changed", "the original root undo journal changed")
		}
		b, err := os.ReadFile(filepath.Join(path, "id"))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) != j.ID {
			return codeFailure("join_changed", "the original root undo step changed")
		}
	}
	return nil
}
