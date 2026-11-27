//go:build linux

package join

import (
	"context"
	"os"
	"os/user"
	"strconv"
	"strings"
)

// The private group is a separate ahead-ledger action. A failed or interrupted
// useradd cannot leave an unrecorded group outside the reverse undo order.
func (e *linuxExecutor) ensurePrivateGroup(ctx context.Context) (int, error) {
	entry := e.ledger.entry("group")
	if group, err := user.LookupGroup(e.account); err == nil {
		if entry == nil || group.Gid != strconv.Itoa(entry.GID) {
			return 0, codeFailure("join_account", "the new account's group already belongs to another owner")
		}
		if err = e.applied(entry); err != nil {
			return 0, err
		}
		return entry.GID, nil
	} else if _, unknown := err.(user.UnknownGroupError); !unknown {
		return 0, err
	}
	if entry == nil {
		gid, err := unusedGID()
		if err != nil {
			return 0, err
		}
		entry, err = e.ahead(ledgerEntry{ID: "group", Kind: "group", GID: gid})
		if err != nil {
			return 0, err
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := command(ctx, nil, "/usr/sbin/groupadd", "--gid", strconv.Itoa(entry.GID), e.account); err != nil {
		return 0, err
	}
	group, err := user.LookupGroup(e.account)
	if err != nil {
		return 0, err
	}
	if group.Gid != strconv.Itoa(entry.GID) {
		return 0, codeFailure("join_changed", "the created account group changed identity")
	}
	return entry.GID, e.applied(entry)
}
func unusedGID() (int, error) {
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 3 {
			gid, _ := strconv.Atoi(fields[2])
			used[gid] = true
		}
	}
	for gid := 1000; gid < 60000; gid++ {
		if !used[gid] {
			return gid, nil
		}
	}
	return 0, codeFailure("join_account", "there is no free private account group id")
}
