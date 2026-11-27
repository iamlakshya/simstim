//go:build linux

package process

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ProbeChild performs the mount and seccomp smoke inside namespaces created
// by Probe. It refuses to change the caller's original mount namespace.
func ProbeChild() error {
	if !ownNamespace() {
		return fmt.Errorf("process probe child needs private user and mount namespaces")
	}
	result := childSmoke()
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return fmt.Errorf("write process probe result: %w", err)
	}
	return nil
}

func childSmoke() smokeResult {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return smokeResult{Error: "make child mounts private"}
	}
	// This mount exists only in the child namespace. No host file is made or
	// changed. Once it succeeds no more path lookups are needed by the test.
	if err := unix.Mount("tmpfs", "/", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=1048576"); err != nil {
		return smokeResult{Error: "mount tmpfs in child"}
	}
	result := smokeResult{UserNS: true}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		result.Error = "set no_new_privs in child"
		return result
	}
	filter := []unix.SockFilter{{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW}}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filter)
	runtime.KeepAlive(program)
	if errno != 0 {
		result.Error = "load seccomp filter in child"
		return result
	}
	result.Seccomp = true
	return result
}
