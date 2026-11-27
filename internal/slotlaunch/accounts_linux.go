//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/brand"
)

func validAccountID(value int) bool { return value > 0 && uint64(value) < uint64(math.MaxUint32) }

func slotAccounts() ([]slotAccount, error) {
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil || len(passwd) > 1<<20 {
		return nil, refused("the slot account database cannot be checked")
	}
	groups, err := os.ReadFile("/etc/group")
	if err != nil || len(groups) > 1<<20 {
		return nil, refused("the slot group database cannot be checked")
	}
	return parseSlotAccounts(passwd, groups)
}

// parseSlotAccounts checks numeric primary groups independently of the optional
// membership list. A differently named or missing primary row cannot provide
// the private group used by the kernel when starting a slot.
func parseSlotAccounts(passwd, groups []byte) ([]slotAccount, error) {
	if len(passwd) > 1<<20 || len(groups) > 1<<20 {
		return nil, refused("the slot account databases exceed their bound")
	}
	accounts := make([]slotAccount, SlotCount)
	used, usedGroups := map[int]bool{}, map[int]bool{}
	for i := range SlotCount {
		name := fmt.Sprintf("%s-s%02d", brand.Name, i)
		matches := 0
		for _, line := range strings.Split(string(passwd), "\n") {
			parts := strings.Split(line, ":")
			if len(parts) != 7 || parts[0] != name {
				continue
			}
			matches++
			uid, e1 := strconv.Atoi(parts[2])
			gid, e2 := strconv.Atoi(parts[3])
			if e1 != nil || e2 != nil || !validAccountID(uid) || !validAccountID(gid) || parts[5] != "/nonexistent" || parts[6] != "/usr/sbin/nologin" || used[uid] || usedGroups[gid] {
				return nil, refused("a slot account has unsafe or reused account fields")
			}
			accounts[i] = slotAccount{uid: uid, gid: gid}
			used[uid], usedGroups[gid] = true, true
		}
		if matches != 1 {
			return nil, refused("a fixed slot account is absent or ambiguous")
		}
		for _, line := range strings.Split(string(passwd), "\n") {
			parts := strings.Split(line, ":")
			if len(parts) != 7 {
				continue
			}
			otherID, err := strconv.Atoi(parts[2])
			if err == nil && otherID == accounts[i].uid && parts[0] != name {
				return nil, refused("a slot uid is also assigned to another local account")
			}
		}
		primaryMatches := 0
		for _, line := range strings.Split(string(groups), "\n") {
			parts := strings.Split(line, ":")
			if len(parts) != 4 {
				continue
			}
			gid, e := strconv.Atoi(parts[2])
			if e != nil || gid < 0 || uint64(gid) >= uint64(math.MaxUint32) {
				return nil, refused("the local group database contains an invalid kernel group ID")
			}
			if (parts[0] == name || parts[0] == "kvm") && !validAccountID(gid) {
				return nil, refused("a slot or KVM group aliases the root group")
			}
			if parts[0] == name {
				primaryMatches++
				if gid != accounts[i].gid {
					return nil, refused("a named slot group differs from its account primary group")
				}
			}
			member := false
			for _, entry := range strings.Split(parts[3], ",") {
				if entry == name {
					member = true
				}
			}
			if gid == accounts[i].gid && parts[0] != name {
				return nil, refused("a slot primary group has another name")
			}
			if member && parts[0] != name && parts[0] != "kvm" {
				return nil, refused("a slot account belongs to an unexpected group")
			}
			if parts[0] == name || parts[0] == "kvm" && member {
				accounts[i].groups = append(accounts[i].groups, uint32(gid))
			}
		}
		if primaryMatches != 1 {
			return nil, refused("a fixed slot primary group is absent or ambiguous")
		}
	}
	return accounts, nil
}

func checkCallerSlotGroups(accounts []slotAccount, gid int) error {
	for _, account := range accounts {
		if account.gid == gid {
			return refused("the login primary group is shared by a slot account; use a private login group for separate accounts")
		}
		for _, group := range account.groups {
			if int(group) == gid {
				return refused("the login primary group is shared by a slot account; use a private login group for separate accounts")
			}
		}
	}
	return nil
}

func purgeSlot(ctx context.Context, account slotAccount) error {
	command := exec.CommandContext(ctx, "/proc/self/exe")
	command.Args = stageArgs("purge-v1")
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(account.uid), Gid: uint32(account.gid), Groups: account.groups}, Setsid: true}
	if err := command.Run(); err != nil {
		return refused("a process of the slot account could not be ended")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		st, err := os.Stat("/proc/" + entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if owned(st, account.uid) {
			return refused("a process of the slot account still runs")
		}
	}
	return nil
}
