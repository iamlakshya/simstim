package client

import (
	"strings"
	"testing"
	"time"
)

func rootSelectionRecordV1(hostID string, mode RootStepMode, status string, ids ...RootStepID) RootStepRecord {
	return RootStepRecord{HostID: hostID, Steps: ids, SHA256: strings.Repeat("c", 64), Mode: mode, At: time.Unix(1, 0).UTC(), Status: status}
}

func TestPurgeRootSelectionUsesOnlyOriginalRunHistory(t *testing.T) {
	hostID := strings.Repeat("a", 32)
	records := []RootStepRecord{rootSelectionRecordV1(hostID, RootRun, "confirmed", RootLinger), rootSelectionRecordV1(hostID, RootRun, "failed", RootKVMGroup), rootSelectionRecordV1(hostID, RootRun, "running", RootLinger), rootSelectionRecordV1(hostID, RootPrint, "printed", RootKVMACL), rootSelectionRecordV1(hostID, RootSkip, "skipped", RootDelegateIO), rootSelectionRecordV1(strings.Repeat("b", 32), RootRun, "confirmed", RootSubuid)}
	ids, err := selectPurgeRootStepsV1(records, hostID)
	if err != nil || len(ids) != 2 || ids[0] != RootKVMGroup || ids[1] != RootLinger {
		t.Fatal("an unrelated, unexecuted or duplicate record selected root mutation", ids, err)
	}
	records[0].Status = "future"
	if _, err := selectPurgeRootStepsV1(records, hostID); err == nil {
		t.Fatal("an unknown root history record granted a selection")
	}
}

func TestPurgeSelectedRootUndoRetainsUnknownEntriesAndHelpers(t *testing.T) {
	original, err := BuildRootScript("fixture", 1000, []RootStepID{RootLinger}, 0)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := filterPurgeRootUndoV1(original.Undo, []RootStepID{RootLinger})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(selected, "        'linger') ;;") || !strings.Contains(selected, "continue ;;\n    esac\n") || !strings.Contains(selected, "Left the root journal helpers") {
		t.Fatal("the selected native loop lost its restriction or retained-helper report")
	}
	for _, sweep := range []string{"rm -f \"$d/applied.json\"", "rmdir \"$d/journal\" \"$d\""} {
		if strings.Contains(selected, sweep) {
			t.Fatal("unselected root helpers entered a sweeping finaliser", sweep)
		}
	}
	if _, err := filterPurgeRootUndoV1(original.Undo+original.Undo, []RootStepID{RootLinger}); err == nil {
		t.Fatal("ambiguous local template anchors selected privileged execution")
	}
}
