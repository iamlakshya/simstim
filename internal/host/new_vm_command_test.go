package host

import (
	"reflect"
	"testing"
)

func TestVMCommandDoesNotReadOrDonateHostExecutablePaths(t *testing.T) {
	for _, input := range [][]string{{"$SHELL", "-l"}, {"agent", "--flag"}, {"/bin/sh", "-c", "printf opaque"}, {"~/tool", "one"}} {
		original := append([]string(nil), input...)
		got := vmCommand(input)
		if !reflect.DeepEqual(input, original) {
			t.Fatal("caller command changed")
		}
		if len(got) != len(input) {
			t.Fatal("command arguments changed")
		}
		for i := 1; i < len(input); i++ {
			if got[i] != input[i] {
				t.Fatal("opaque argument changed")
			}
		}
	}
	if got := vmCommand([]string{"$SHELL"}); got[0] != "/bin/sh" {
		t.Fatal(got)
	}
	if got := vmCommand([]string{"~/tool"}); got[0] != GuestHome+"/tool" {
		t.Fatal(got)
	}
}

func TestChosenWallKeepsExplicitFloorAndDowngrade(t *testing.T) {
	for _, a := range []NewArgs{{Wall: "process", Floor: "microvm"}, {Wall: "process", Floor: "microvm", Downgrade: true}, {Wall: "process", Downgrade: false, DowngradedFrom: "microvm"}} {
		if err := validateChosenWall(a); err == nil {
			t.Fatal("incomplete downgrade accepted", a.Wall, a.Floor)
		}
	}
	if err := validateChosenWall(NewArgs{Wall: "process", Floor: "microvm", Downgrade: true, DowngradedFrom: "microvm"}); err != nil {
		t.Fatal(err)
	}
}
