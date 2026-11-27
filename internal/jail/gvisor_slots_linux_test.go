package jail

import (
	"encoding/binary"
	"testing"
)

func TestGvisorPublicSlotPreludeRejectsMalformedVersionAndCount(t *testing.T) {
	makePrelude := func(count uint32) []byte {
		data := []byte{'S', 'L', 'T', 1, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(data[4:], count)
		return data
	}
	for _, count := range []uint32{6, 17, 1024} {
		got, err := parseGvisorSlotPreludeV1(makePrelude(count))
		if err != nil || got != int(count) {
			t.Fatal("exact public original layout rejected", count, got, err)
		}
	}
	badVersion := makePrelude(17)
	badVersion[3] = 2
	for _, bad := range [][]byte{nil, makePrelude(5), makePrelude(1025), makePrelude(^uint32(0)), badVersion, append(makePrelude(17), 0), {'G', 0, 0, 0, 17}} {
		if _, err := parseGvisorSlotPreludeV1(bad); err == nil {
			t.Fatal("a malformed or future public count passed", bad)
		}
	}
}

func TestGvisorPrivateVectorDerivesEveryRoleFromOriginalSetup(t *testing.T) {
	c := &setup{GvisorV1Version: 1, Files: 1, StatusFD: 5, Binds: []bind{{FD: 6}, {FD: 7}}}
	expected, err := expectedGvisorSlotsV1(c, stageInit)
	if err != nil || !equalGvisorSlotsV2(expected, []int{0, 1, 2, 3, 5, 6, 7}) {
		t.Fatal("original exact init roles rejected", expected, err)
	}
	for _, bad := range []*setup{{GvisorV1Version: 1, Files: 1, StatusFD: 4}, {GvisorV1Version: 1, Files: 1, StatusFD: 5, Binds: []bind{{FD: 5}}}, {GvisorV1Version: 1, Files: 1, StatusFD: 5, Binds: []bind{{FD: maxGvisorSlotsV2}}}, {GvisorV1Version: 1, Files: -1}, {GvisorV1Version: 1, Files: 1, SyncFD: 7, StatusFD: 5}} {
		if _, err := expectedGvisorSlotsV1(bad, stageInit); err == nil {
			t.Fatal("an extra/repeated/partial native role layout passed", bad)
		}
	}
	nested := *c
	nested.StatusFD = 0
	expected, err = expectedGvisorSlotsV1(&nested, stageExec)
	if err != nil || !equalGvisorSlotsV2(expected, []int{0, 1, 2, 3, 6, 7}) {
		t.Fatal("nested runtime acquired parent status role", expected, err)
	}
	if _, err := expectedGvisorSlotsV1(c, stageExec); err == nil {
		t.Fatal("runtime adopted parent status role")
	}
}
