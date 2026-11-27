//go:build linux

package jail

import "testing"

// No native capability is fabricated here. These cases must refuse before
// touching any FD or accepting private mode metadata through a legacy stage.
func TestVMMPrivateStagesRejectLegacyAndMixedPurposesV1(t *testing.T) {
	for _, stage := range []string{stageOuter, stageInit, stageExec, stageOuterFilesV1, stageInitFilesV1, stageExecFilesV1, stageOuterBindsV1, stageInitBindsV1, stageExecBindsV1, stageInitGvisorV1, stageExecGvisorV1, stageSlotInitLeaseV1} {
		set := &setup{VMMExecV1: &vmmExecSetupV1{Version: 1}}
		if _, err := checkFileRulesStageV1(stage, set); err == nil {
			t.Fatal("a legacy or foreign-purpose stage accepted private VMM metadata", stage)
		}
	}
	for _, pair := range [][2]string{{stageOuter, stageOuterVMMV1}, {stageInit, stageInitVMMV1}, {stageExec, stageExecVMMV1}} {
		set := &setup{FilesV1Version: 1, VMMExecV1: &vmmExecSetupV1{Version: 1}}
		if got := fileRulesStageV1(pair[0], set); got != pair[1] {
			t.Fatal("the original private VMM stage would enter a frozen legacy helper", got)
		}
		// A distinct name is still only metadata. The missing native layout and
		// actual private capabilities must refuse before any stage can execute.
		if _, err := checkFileRulesStageV1(pair[1], set); err == nil {
			t.Fatal("a stage name alone created a private mode capability", pair[1])
		}
	}
}
