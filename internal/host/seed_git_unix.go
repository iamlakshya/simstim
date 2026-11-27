//go:build linux || darwin

package host

import (
	"os"
	"syscall"
)

func systemGit() (string, error) {
	for _, p := range []string{"/usr/bin/git", "/bin/git"} {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if ok && owner.Uid == 0 && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0111 != 0 {
			return p, nil
		}
	}
	return "", UserError("seed_git", "the host has no trusted system Git executable", "Install Git through the host's package manager.")
}
