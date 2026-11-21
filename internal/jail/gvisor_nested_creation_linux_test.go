package jail

import (
	"testing"
)

func TestGvisorCreationPIDFDRequiresUnambiguousNativeAncestry(t *testing.T) {
	for _, good := range []string{"Pid:\t41\nNSpid:\t41\t1\n", "pos:\t0\nflags:\t02000002\nPid:\t41\nNSpid:\t41\t9\t1\n"} {
		pids, err := parseGvisorPIDFDInfoV1([]byte(good))
		if err != nil || pids[0] != 41 || pids[len(pids)-1] != 1 {
			t.Fatal("original native PID ancestry was refused", pids, err)
		}
	}
	for _, bad := range []string{"", "Pid:\t41\n", "Pid:\t-1\nNSpid:\t-1\n", "Pid:\t41\nPid:\t41\nNSpid:\t41\t1\n", "Pid:\t41\nNSpid:\t41\t1\nNSpid:\t41\t1\n", "Pid:\t41\nNSpid:\nNSpid:\t41\t1\n", "Pid:\t41\nNSpid:\t40\t1\n", "Pid:\t41\nNSpid:\t41\t2\n", "Pid:\t041\nNSpid:\t041\t1\n", "Pid:\t41\nNSpid:\t41\t+2\t1\n", "Pid:\t41\nNSpid:\t41\t0\t1\n"} {
		if pids, err := parseGvisorPIDFDInfoV1([]byte(bad)); err == nil {
			t.Fatal("ambiguous or non-native task data passed", bad, pids)
		}
	}
}

func TestGvisorNestedCustodyJoinsBothProcessViewsToOriginalCreator(t *testing.T) {
	local := gvisorTaskV2{pid: 7, parent: 1, birth: 100}
	creator := gvisorTaskV2{pid: 1, parent: 0, birth: 90}
	outside := gvisorTaskV2{pid: 47, parent: 40, birth: 100}
	original := gvisorTaskV2{pid: 40, parent: 39, birth: 90}
	if err := checkGvisorNestedTasksV1(local, creator, outside, original, 47, []int{47, 7, 1}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*gvisorTaskV2, *gvisorTaskV2, *gvisorTaskV2, *gvisorTaskV2){
		func(a, b, c, d *gvisorTaskV2) { a.birth++ },
		func(a, b, c, d *gvisorTaskV2) { a.parent++ },
		func(a, b, c, d *gvisorTaskV2) { b.birth++ },
		func(a, b, c, d *gvisorTaskV2) { b.pid++ },
		func(a, b, c, d *gvisorTaskV2) { b.parent++ },
		func(a, b, c, d *gvisorTaskV2) { c.parent++ },
		func(a, b, c, d *gvisorTaskV2) { c.pid++ },
		func(a, b, c, d *gvisorTaskV2) { d.birth++ },
	} {
		a, b, c, d := local, creator, outside, original
		mutate(&a, &b, &c, &d)
		if err := checkGvisorNestedTasksV1(a, b, c, d, 47, []int{47, 7, 1}); err == nil {
			t.Fatal("a substituted native birth or creator chain passed", a, b, c, d)
		}
	}
	for _, pids := range [][]int{nil, {47, 1}, {47, 8, 1}, {47, 7, 2}, {47, 7, 1, 1}} {
		if err := checkGvisorNestedTasksV1(local, creator, outside, original, 47, pids); err == nil {
			t.Fatal("changed native namespace ancestry passed", pids)
		}
	}
}

func TestGvisorPendingAndCopiedCreationCannotGrantLifetime(t *testing.T) {
	for _, source := range []*gvisorNestedCreationV1{nil, {}, {birth: gvisorTaskV2{pid: 7}}} {
		if source.liveV1() == nil || source.readyV1() == nil || source.joinV1() == nil || source.closeJoinedV1() == nil {
			t.Fatal("missing native custody became live or joined", source)
		}
	}
	outer := &gvisorOuterOwnerV1{}
	original := &gvisorNestedCreationV1{outer: outer}
	outer.nested = original
	original.original = original
	copy := gvisorNestedCreationV1{outer: outer, original: original}
	if err := copy.checkV1(); err == nil {
		t.Fatal("copied data adopted another original creation lifetime")
	}
	if err := reportGvisorNestedCreationV1(&setup{GvisorV1Version: 1}, nil, nil); err == nil {
		t.Fatal("a serialised purpose acquired the original post-map report channel")
	}
}

func TestGvisorCreationControlCannotCarryPlanOrGuestData(t *testing.T) {
	for _, message := range []gvisorMessageV2{{SlotCount: 6}, {SocketFD: 3}, {Setup: &setup{Files: 1}}, {Slots: []int{3}}} {
		if emptyGvisorControlV1(message) {
			t.Fatal("another setup role was accepted as original creation custody", message)
		}
	}
}
