//go:build linux

package supply

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeMemfdLegacyFallbackDoesNotBypassPolicyErrors(t *testing.T) {
	for _, kind := range []string{KindVMM, KindKernel, KindGvisor} {
		for _, first := range []error{nil, unix.EINVAL, unix.EACCES, unix.EPERM, unix.ENOMEM} {
			t.Run(kind+"/"+runtimeSealErrorName(first), func(t *testing.T) {
				calls := 0
				fd, err := createRuntimeMemfd(kind, func(name string, flags int) (int, error) {
					calls++
					base := unix.MFD_CLOEXEC | unix.MFD_ALLOW_SEALING
					want := base | unix.MFD_NOEXEC_SEAL
					if kind == KindVMM || kind == KindGvisor {
						want = base | unix.MFD_EXEC
					}
					if calls == 2 {
						want = base
					}
					if name != "runtime-"+kind || flags != want || calls > 2 {
						t.Fatal("creation changed its closed flags or repeated a failed operation", name, flags, calls)
					}
					if calls == 1 && first != nil {
						return -1, first
					}
					return 42, nil
				})
				if first == nil || errors.Is(first, unix.EINVAL) {
					wantCalls := 1
					if errors.Is(first, unix.EINVAL) {
						wantCalls = 2
					}
					if fd != 42 || err != nil || calls != wantCalls {
						t.Fatal("supported byte sealing was refused", fd, err)
					}
				} else if fd != -1 || !errors.Is(err, first) || calls != 1 {
					t.Fatal("a policy or resource failure retried with weaker flags", fd, err, calls)
				}
			})
		}
	}
}

func TestRuntimeSealLegacyFallbackKeepsEveryByteSeal(t *testing.T) {
	for _, first := range []error{nil, unix.EINVAL, unix.EPERM, unix.EBUSY} {
		t.Run(runtimeSealErrorName(first), func(t *testing.T) {
			calls := 0
			err := sealRuntimeMemfd(42, KindVMM, func(fd uintptr, command, seals int) (int, error) {
				calls++
				if fd != 42 || command != unix.F_ADD_SEALS || seals&runtimeSeals != runtimeSeals {
					t.Fatal("the original descriptor or mandatory byte seals changed", fd, command, seals)
				}
				if calls == 1 {
					if seals&unix.F_SEAL_EXEC == 0 {
						t.Fatal("creation skipped the modern execution seal")
					}
					return 0, first
				}
				if calls != 2 || !errors.Is(first, unix.EINVAL) || seals != runtimeSeals {
					t.Fatal("a failed seal admitted an unrelated fallback", calls, first, seals)
				}
				return 0, nil
			})
			if errors.Is(first, unix.EINVAL) {
				if err != nil || calls != 2 {
					t.Fatal("legacy sealing lost its immutable byte contract", err, calls)
				}
			} else if !errors.Is(err, first) || calls != 1 {
				t.Fatal("a seal failure was ignored or retried", err, calls)
			}
		})
	}
}

func runtimeSealErrorName(err error) string {
	if err == nil {
		return "success"
	}
	return err.Error()
}
