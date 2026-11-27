package jail

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/slotlease"
)

func TestSlotCommandRequiresActualOpaqueSourceBeforeEffects(t *testing.T) {
	if _, err := CommandSlotV1(context.Background(), Spec{Seccomp: ProfileVMM}, nil); err == nil {
		t.Fatal("a nil source selected the closed root lease purpose")
	}
	var generic Cmd
	if _, err := generic.RetainSlotInitV1(&slotlease.SourceV1{}); err == nil {
		t.Fatal("a generic command produced slot init native evidence")
	}
}

func TestSlotLeaseCannotReachFinalExecOrOldStage(t *testing.T) {
	init := &setup{InitLeaseV1Version: 1, InitLeaseFDV1: 7, InitLeaseRefV1: &slotInitLeaseRefV1{Dev: 1, Ino: 2}, Files: 1, Binds: []bind{{FD: 6}}, Userns: true, Seccomp: ProfileVMM}
	if stage, err := checkSlotInitLeaseStageV1(stageSlotInitLeaseV1, init); err != nil || stage != stageInit {
		t.Fatal("closed namespace init rejected", stage, err)
	}
	for _, old := range []string{stageInit, stageOuter, stageExec, stageInitFilesV1, stageInitGvisorV1} {
		if _, err := checkSlotInitLeaseStageV1(old, init); err == nil {
			t.Fatal("an old/unrelated stage adopted root init lease", old)
		}
	}
	final := setupForFinalExecV1(init)
	if final.InitLeaseV1Version != 0 || final.InitLeaseFDV1 != 0 || final.InitLeaseRefV1 != nil || final.Files != 1 || final.Binds[0].FD != 6 {
		t.Fatal("final exec gained root lease or lost ordinary files")
	}
	if _, err := checkSlotInitLeaseStageV1(stageExec, &final); err != nil {
		t.Fatal(err)
	}
	invalid := *init
	invalid.Userns = false
	if _, err := checkSlotInitLeaseStageV1(stageSlotInitLeaseV1, &invalid); err == nil {
		t.Fatal("LL-only fallback claimed namespace lifetime")
	}
	invalid = *init
	invalid.InitLeaseV1Version = 2
	if _, err := checkSlotInitLeaseStageV1(stageSlotInitLeaseV1, &invalid); err == nil {
		t.Fatal("future lease version executed")
	}
	invalid = *init
	invalid.InitLeaseFDV1 = 3
	if _, err := checkSlotInitLeaseStageV1(stageSlotInitLeaseV1, &invalid); err == nil {
		t.Fatal("init lease entered final Files range")
	}
}
