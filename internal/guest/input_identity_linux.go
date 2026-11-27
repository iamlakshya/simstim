//go:build linux

package guest

import (
	"os"
	"syscall"
)

func checkInputHelperAccount() error {
	if os.Geteuid() != AgentUID || os.Getegid() != AgentGID {
		return inputError("the input receiver requires the fixed guest account")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group != AgentGID {
			return inputError("the input receiver inherited an unapproved group")
		}
	}
	return nil
}

func inputOwner(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}
