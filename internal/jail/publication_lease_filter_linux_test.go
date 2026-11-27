//go:build linux

package jail

import (
	"testing"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

func TestPublicationWorkerCannotDropOrUnlockOriginalFenceV1(t *testing.T) {
	const fd = 137
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			program, err := publicationFenceFilterV1(architecture, fd)
			if err != nil {
				t.Fatal(err)
			}
			machine, err := bpf.NewVM(program)
			if err != nil {
				t.Fatal(err)
			}
			info, err := arch.GetInfo(architecture)
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name      string
				arguments [6]uint64
				deny      bool
			}{
				{"close", [6]uint64{fd}, true}, {"close", [6]uint64{1<<32 | fd}, true}, {"close", [6]uint64{fd - 1}, false},
				{"dup3", [6]uint64{4, fd}, true}, {"dup3", [6]uint64{fd, fd + 1}, false},
				{"close_range", [6]uint64{0, 1<<32 - 1}, true}, {"close_range", [6]uint64{fd, fd}, true},
				{"close_range", [6]uint64{0, fd - 1}, false}, {"close_range", [6]uint64{fd + 1, 1<<32 - 1}, false},
				{"fcntl", [6]uint64{fd, unix.F_SETFD, unix.FD_CLOEXEC}, true},
				{"fcntl", [6]uint64{fd, 1<<32 | unix.F_SETLK}, true},
				{"fcntl", [6]uint64{fd + 1, unix.F_SETFD}, false}, {"fcntl", [6]uint64{fd, unix.F_GETFL}, false},
				{"flock", [6]uint64{fd, unix.LOCK_UN}, true}, {"flock", [6]uint64{fd + 1, unix.LOCK_EX}, true},
				{"execve", [6]uint64{}, true}, {"execveat", [6]uint64{}, true},
				{"write", [6]uint64{4}, false}, {"read", [6]uint64{3}, false},
			} {
				data := seccompData(uint32(info.SyscallNames[test.name]), uint32(info.ID), test.arguments)
				result, err := machine.Run(data)
				if err != nil {
					t.Fatal(err)
				}
				refused := uint32(result) == retErrno|errnoEPERM
				if refused != test.deny {
					t.Fatalf("%s%v denied=%v want=%v", test.name, test.arguments, refused, test.deny)
				}
			}
		})
	}
}

func TestPublicationInitialExecRemainsPossibleWithUndroppableFenceV1(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		program, err := publicationFencePhaseFilterV1(architecture, 200, false)
		if err != nil {
			t.Fatal(err)
		}
		machine, err := bpf.NewVM(program)
		if err != nil {
			t.Fatal(err)
		}
		info, err := arch.GetInfo(architecture)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"execve", "execveat", "close"} {
			data := seccompData(uint32(info.SyscallNames[name]), uint32(info.ID), [6]uint64{200})
			result, err := machine.Run(data)
			if err != nil {
				t.Fatal(err)
			}
			want := uint32(retAllow)
			if name == "close" {
				want = retErrno | errnoEPERM
			}
			if uint32(result) != want {
				t.Fatalf("%s initial %s returned %#x, want %#x", architecture, name, result, want)
			}
		}
	}
}
