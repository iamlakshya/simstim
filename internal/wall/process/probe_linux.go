//go:build linux

package process

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

func landlockABI() int {
	// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)
	// returns the highest supported Landlock ABI version without creating
	// a ruleset or requiring privileges.
	r1, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(r1)
}

func seccompOK() bool {
	if !hasProcSeccompLine("/proc/self/status") {
		return false
	}
	// Check that seccomp filters can be queried (CONFIG_SECCOMP_FILTER)
	// without installing a filter on this process.
	action := uint32(unix.SECCOMP_RET_ERRNO)
	//nolint:gosec // G103: SECCOMP_GET_ACTION_AVAIL takes a pointer to a uint32 action.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_GET_ACTION_AVAIL, 0, uintptr(unsafe.Pointer(&action)))
	return errno == 0
}

func hasProcSeccompLine(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "Seccomp:") {
			return true
		}
	}
	return false
}
