//go:build linux

package guest

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/memberenv"
	"golang.org/x/sys/unix"
)

// checkNamedSSHHelperProjectionV1 compares native objects, not executable
// spellings. A held-FD or memfd exec may have another kernel-reported name.
func checkNamedSSHHelperProjectionV1() error {
	root, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return execError("the guest SSH helper root cannot be opened")
	}
	defer unix.Close(root)
	projected, err := unix.Openat2(root, strings.TrimPrefix(memberenv.NamedSSHHelperPath(), "/"), &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return execError("the fixed guest SSH helper projection cannot be opened")
	}
	defer unix.Close(projected)
	// This is the current process's own magic link. No peer PID or caller
	// path can choose the executable whose native identity is compared.
	current, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return execError("the current guest helper identity cannot be opened")
	}
	defer unix.Close(current)
	var mapped, running unix.Stat_t
	var mount unix.Statfs_t
	if unix.Fstat(projected, &mapped) != nil || unix.Fstat(current, &running) != nil || unix.Fstatfs(projected, &mount) != nil ||
		mapped.Mode&unix.S_IFMT != unix.S_IFREG || running.Mode&unix.S_IFMT != unix.S_IFREG ||
		mapped.Dev != running.Dev || mapped.Ino != running.Ino || mount.Flags&unix.ST_RDONLY == 0 {
		return execError("the fixed SSH transport is not the current helper's read-only native projection")
	}
	return nil
}
