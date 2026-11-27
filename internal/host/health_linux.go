//go:build linux

package host

import (
	"math"
	"os"
	"os/user"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func hostHealthDiskTotalV1(path string) int64 {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil || stat.Bsize <= 0 || stat.Blocks > uint64(math.MaxInt64)/uint64(stat.Bsize) {
		return 0
	}
	return int64(stat.Blocks) * stat.Bsize
}

func hostHealthNativeV1() (reboot, linger, lingerKnown bool) {
	info, err := os.Stat("/var/run/reboot-required")
	reboot = err == nil && info.Mode().IsRegular()
	u, err := user.Current()
	if err != nil || u.Username == "" || filepath.Base(u.Username) != u.Username {
		return reboot, false, false
	}
	info, err = os.Lstat(filepath.Join("/var/lib/systemd/linger", u.Username))
	if os.IsNotExist(err) {
		// Linger has meaning only when this machine has systemd state.
		manager, managerErr := os.Stat("/run/systemd/system")
		return reboot, false, managerErr == nil && manager.IsDir()
	}
	return reboot, err == nil && info.Mode().IsRegular(), err == nil
}
