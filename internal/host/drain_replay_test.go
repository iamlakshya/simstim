package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// These are lower filesystem replay oracles. They issue no helper entry,
// client Source, native session operation, unit absence or boot authority.
func drainReplayFixtureV1(t *testing.T) (*os.Root, *os.Root, *placementControl) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	statePath, guardPath := filepath.Join(base, "state"), filepath.Join(base, "guard")
	state, err := cachefs.OpenPrivateRoot(statePath, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	guard, err := cachefs.OpenPrivateRoot(guardPath, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	if err := os.WriteFile(filepath.Join(statePath, "place.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stateInfo, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	guardInfo, err := os.Stat(guardPath)
	if err != nil {
		t.Fatal(err)
	}
	placeInfo, err := state.Stat("place.lock")
	if err != nil {
		t.Fatal(err)
	}
	control := &placementControl{HostID: strings.Repeat("a", 32), State: refOf(stateInfo), Place: refOf(placeInfo), Control: refOf(guardInfo), Refs: map[string]purgeRef{}}
	data, err := json.Marshal(userDrainV1{Version: 1, HostID: control.HostID, State: control.State, Place: control.Place, Reason: "maintenance", At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statePath, "drain.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := state.Stat("drain.json")
	if err != nil {
		t.Fatal(err)
	}
	control.Refs["state/drain.json"] = refOf(info)
	control.DrainRemoval = &drainRemovalV1{Ref: refOf(info), Capture: ".drain-clear-" + strings.Repeat("b", 32)}
	return state, guard, control
}

func TestDrainClearReplaysExactNativeCaptureAfterInterruptedMove(t *testing.T) {
	state, guard, control := drainReplayFixtureV1(t)
	if err := state.Rename("drain.json", control.DrainRemoval.Capture); err != nil {
		t.Fatal(err)
	}
	if err := replayDrainRemovalV1(t.Context(), guard, control, state); err != nil {
		t.Fatal(err)
	}
	if control.DrainRemoval != nil {
		t.Fatal("completed clear kept its pending intent")
	}
	if _, found := control.Refs["state/drain.json"]; found {
		t.Fatal("completed clear kept its producer receipt")
	}
	directory, err := state.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if entries, err := directory.ReadDir(-1); err != nil || len(entries) != 1 || entries[0].Name() != "place.lock" {
		t.Fatal("clear retained or removed an unrelated file", entries, err)
	}
}

func TestDrainClearPreservesReplacementWhileOriginalCaptureWaits(t *testing.T) {
	state, guard, control := drainReplayFixtureV1(t)
	capture := control.DrainRemoval.Capture
	if err := state.Rename("drain.json", capture); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement must remain byte exact")
	if err := os.WriteFile(filepath.Join(state.Name(), "drain.json"), replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replayDrainRemovalV1(t.Context(), guard, control, state); err == nil {
		t.Fatal("a replacement was adopted as the original drain file")
	}
	got, err := state.ReadFile("drain.json")
	if err != nil || string(got) != string(replacement) {
		t.Fatal("clear changed the replacement", string(got), err)
	}
	if _, err := state.Stat(capture); errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed clear discarded its original capture")
	}
}
