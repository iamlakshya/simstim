//go:build linux

package supply

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Older supported hosts have sealing but not the execution flags added in
// Linux 6.3. EACCES, including an enforced memfd policy, never permits a retry.
func createRuntimeMemfd(kind string, create func(string, int) (int, error)) (int, error) {
	base := unix.MFD_CLOEXEC | unix.MFD_ALLOW_SEALING
	flags := base | unix.MFD_NOEXEC_SEAL
	if runtimeExecutableKind(kind) {
		flags = base | unix.MFD_EXEC
	}
	fd, err := create("runtime-"+kind, flags)
	if errors.Is(err, unix.EINVAL) {
		return create("runtime-"+kind, base)
	}
	return fd, err
}

func sealRuntimeMemfd(fd uintptr, kind string, seal func(uintptr, int, int) (int, error)) error {
	want := runtimeSeals
	if runtimeExecutableKind(kind) {
		want |= unix.F_SEAL_EXEC
	}
	_, err := seal(fd, unix.F_ADD_SEALS, want)
	if runtimeExecutableKind(kind) && errors.Is(err, unix.EINVAL) {
		_, err = seal(fd, unix.F_ADD_SEALS, runtimeSeals)
	}
	return err
}

func runtimeExecutableKind(kind string) bool {
	return kind == KindVMM || kind == KindGvisor
}
