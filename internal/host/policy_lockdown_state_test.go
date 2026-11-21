package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// These lower filesystem replay tests issue no helper entry, native VM or
// session operation. The production publisher still requires its real owner.
func lockdownReplayFixtureV1(t *testing.T) (*os.Root, *os.Root, *placementControl) {
	t.Helper()
	state, guard, control := drainReplayFixtureV1(t)
	data, err := json.Marshal(hostLockdownRecordV1{Version: 1, HostID: control.HostID, State: control.State, Place: control.Place, At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.Name(), string(stateLockdown)), data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := state.Stat(string(stateLockdown))
	if err != nil {
		t.Fatal(err)
	}
	control.Refs["state/"+string(stateLockdown)] = refOf(info)
	control.LockdownRemoval = &lockdownRemovalV1{Ref: refOf(info), Capture: ".lockdown-clear-" + strings.Repeat("c", 32)}
	return state, guard, control
}

func TestLockdownClearReplaysOnlyItsOriginalCapturedNativeFile(t *testing.T) {
	state, guard, control := lockdownReplayFixtureV1(t)
	drainBefore, err := state.ReadFile("drain.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Rename(string(stateLockdown), control.LockdownRemoval.Capture); err != nil {
		t.Fatal(err)
	}
	if err := replayLockdownRemovalV1(t.Context(), guard, control, state); err != nil {
		t.Fatal(err)
	}
	if control.LockdownRemoval != nil {
		t.Fatal("completed clear retained its ahead intent")
	}
	if _, exists := control.Refs["state/"+string(stateLockdown)]; exists {
		t.Fatal("completed clear retained its original producer receipt")
	}
	if _, err := state.Stat(string(stateLockdown)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cleared lockdown file remains", err)
	}
	if after, err := state.ReadFile("drain.json"); err != nil || string(after) != string(drainBefore) {
		t.Fatal("lockdown clear changed unrelated native metadata", err)
	}
}

func TestLockdownClearPreservesReplacementAndOriginalCapture(t *testing.T) {
	state, guard, control := lockdownReplayFixtureV1(t)
	capture := control.LockdownRemoval.Capture
	if err := state.Rename(string(stateLockdown), capture); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("a replacement is not the original native lockdown receipt")
	if err := os.WriteFile(filepath.Join(state.Name(), string(stateLockdown)), replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := replayLockdownRemovalV1(t.Context(), guard, control, state); err == nil {
		t.Fatal("replacement native file was adopted for off")
	}
	if after, err := state.ReadFile(string(stateLockdown)); err != nil || string(after) != string(replacement) {
		t.Fatal("off changed the replacement", err)
	}
	if _, err := state.Stat(capture); err != nil {
		t.Fatal("failed off discarded the original capture", err)
	}
}

func TestLockdownLayerRemovalPreservesAllUnderlyingSessionRules(t *testing.T) {
	layers := []api.Layer{{Kind: api.LayerAll, Rules: []api.Rule{{Allow: true, Target: "broader.example"}}}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "session.example", GrantID: "original"}, {Allow: false, Target: "denied.example"}}}}
	_, clean, err := ApplyPolicyLockdownLayersV1(layers, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := api.HashPolicyLayerVectorV1(clean)
	if err != nil {
		t.Fatal(err)
	}
	locked, restricted, err := ApplyPolicyLockdownLayersV1(clean, true)
	if err != nil || locked.Preset != api.PresetLocked || locked.Unknown != api.UnknownDeny {
		t.Fatal("native restriction was not projected as actual locked policy", err)
	}
	_, restored, err := ApplyPolicyLockdownLayersV1(restricted, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := api.HashPolicyLayerVectorV1(restored)
	byTarget := map[string]api.Rule{}
	for _, layer := range restored {
		for _, rule := range layer.Rules {
			byTarget[rule.Target] = rule
		}
	}
	if err != nil || before != after || byTarget["session.example"].GrantID != "original" || byTarget["denied.example"].Allow {
		t.Fatal("off replaced an underlying allowance, deny or instance", err)
	}
}
