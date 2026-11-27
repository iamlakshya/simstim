//go:build linux

package join

import (
	"os"
	"os/user"
	"strconv"

	"github.com/iamlakshya/simstim/internal/brand"
)

func accountOrigin(entry *ledgerEntry) string { return brand.Name + " joined-" + entry.Origin }

// verifyCreatedAccount requires the original uid, primary group, home inode
// and root-created account marker. A reused name/uid/home pathname is not an
// original account and may never be terminated by leave.
func (e *linuxExecutor) verifyCreatedAccount(entry *ledgerEntry, capture bool) (*user.User, error) {
	if entry == nil || entry.ID != "account" || entry.UID <= 0 || entry.GID <= 0 || len(entry.Origin) != 26 {
		return nil, codeFailure("join_changed", "the original created account identity is incomplete")
	}
	u, err := user.Lookup(e.account)
	if err != nil {
		return nil, err
	}
	if u.Uid != strconv.Itoa(entry.UID) || u.Gid != strconv.Itoa(entry.GID) || u.HomeDir != entry.Home || u.Name != accountOrigin(entry) {
		return nil, codeFailure("join_changed", "the created account was replaced or changed; it was left in place")
	}
	if err = safeHome(entry.Home, entry.UID); err != nil {
		return nil, err
	}
	st, err := os.Lstat(entry.Home)
	if err != nil {
		return nil, err
	}
	owner, group := fileOwner(st)
	if owner != entry.UID || group != entry.GID {
		return nil, codeFailure("join_changed", "the original created home changed owner")
	}
	dev, ino, err := identity(entry.Home)
	if err != nil {
		return nil, err
	}
	if entry.Inode == 0 {
		if !capture {
			return nil, codeFailure("join_changed", "the created home has no original native identity")
		}
		entry.Device, entry.Inode = dev, ino
		if err = e.save(); err != nil {
			return nil, err
		}
	} else if entry.Device != dev || entry.Inode != ino {
		return nil, codeFailure("join_changed", "the original created home was replaced")
	}
	return u, nil
}
