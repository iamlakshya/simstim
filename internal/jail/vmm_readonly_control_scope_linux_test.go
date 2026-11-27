//go:build linux

package jail

import (
	"testing"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

func TestReadonlyControlCannotExecVMMOrEnableDumpabilityV1(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		program, err := readonlyControlFilterV1(architecture)
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
			name string
			args [6]uint64
			deny bool
		}{
			{"execve", [6]uint64{}, true}, {"execveat", [6]uint64{3}, true},
			{"prctl", [6]uint64{unix.PR_SET_DUMPABLE, 1}, true}, {"prctl", [6]uint64{unix.PR_SET_DUMPABLE, 2}, true},
			{"prctl", [6]uint64{1<<32 | unix.PR_SET_DUMPABLE, 1}, true},
			{"prctl", [6]uint64{unix.PR_SET_DUMPABLE, 0}, false}, {"prctl", [6]uint64{unix.PR_GET_DUMPABLE}, false},
			{"read", [6]uint64{9}, false}, {"write", [6]uint64{10}, false}, {"close", [6]uint64{17}, false},
		} {
			result, err := machine.Run(seccompData(uint32(info.SyscallNames[test.name]), uint32(info.ID), test.args))
			if err != nil {
				t.Fatal(err)
			}
			denied := uint32(result) == retErrno|errnoEPERM
			if denied != test.deny {
				t.Fatalf("%s %s%v denied=%v want=%v", architecture, test.name, test.args, denied, test.deny)
			}
		}
	}
}
