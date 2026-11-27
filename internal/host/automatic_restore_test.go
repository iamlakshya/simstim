package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestAutomaticControllerSelectionPreservesEndedPrimaryAndOnlyActiveChildren(t *testing.T) {
	primary := autoRestoreFixture("")
	primary.Status.State = StateExited
	primary.Life.End.Claimed = true // primary's natural End preceded System stop
	active := autoRestoreFixture(primary.ID)
	ended := autoRestoreFixture(primary.ID)
	ended.Life.End.Claimed, ended.Status.State = true, StateExited
	for _, m := range []*Manifest{primary, active, ended} {
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(m.ID)) })
	}
	operation, err := BeginSessionOperation(context.Background(), primary.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	selection, err := SelectAutomaticRestore(primary)
	if err != nil || selection == nil || selection.PrimaryWanted || !slices.Equal(selection.MemberIDs, []string{active.ID}) {
		t.Fatalf("wrong controller-owned selection: %+v %v", selection, err)
	}
	primary.Run.BootID = "next-boot"
	primary.Status.State = StateStarting
	if err := PinAutomaticRestore(primary, selection); err != nil {
		t.Fatal(err)
	}
	if err := primary.Save(); err != nil {
		t.Fatal(err)
	}
	plan, err := ValidateAutomaticRestore(primary)
	if err != nil || plan.PrimaryWanted || plan.PrimaryStatus.State != StateExited || !slices.Equal(plan.MemberIDs, []string{active.ID}) {
		t.Fatalf("staging restarted or lost the ended primary: %+v %v", plan, err)
	}
	command, _, err := sessionCommand(primary)
	if err != nil || command != "supervise-restore-v1" {
		t.Fatal("old helper could silently use ordinary supervise", err)
	}
	before := bytes.Clone(primary.AutomaticRestore)
	ended.CreatedAt = ended.CreatedAt.Add(time.Second) // same ID, changed identity
	if err := ended.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateAutomaticRestore(primary); err == nil || !bytes.Equal(before, primary.AutomaticRestore) {
		t.Fatal("changed unselected membership was adopted or mutated the plan")
	}
}

func TestAutomaticControllerZeroEligibleDoesNotInventPrimaryWork(t *testing.T) {
	primary := autoRestoreFixture("")
	primary.Status.State, primary.Life.End.Claimed = StateExited, true
	child := autoRestoreFixture(primary.ID)
	child.Status.State, child.Life.End.Claimed = StateExited, true
	for _, m := range []*Manifest{primary, child} {
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(m.ID)) })
	}
	selection, err := SelectAutomaticRestore(primary)
	if err != nil || selection.PrimaryWanted || len(selection.MemberIDs) != 0 {
		t.Fatal("zero eligible members produced a primary command", err)
	}
	if err := PinAutomaticRestore(primary, selection); err == nil || len(primary.AutomaticRestore) != 0 {
		t.Fatal("an empty controller-only wall was published")
	}
}

func TestAutomaticPlanCodecRejectsAmbiguousAndFutureInputsWithoutMutation(t *testing.T) {
	plan := AutomaticRestorePlan{Version: 1, Snapshot: api.SandboxSnapshot{Version: 1, Primary: "a2a2a2a2", Generation: strings.Repeat("a", 64), Members: []string{"b3b3b3b3", "c4c4c4c4"}}, PrimaryStatus: Status{State: StateExited}, MemberIDs: []string{"b3b3b3b3"}}
	raw, _ := json.Marshal(plan)
	if decoded, err := DecodeAutomaticRestore(raw); err != nil || decoded.PrimaryWanted {
		t.Fatal("explicit false controller intent was lost", err)
	}
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1),
		bytes.Replace(raw, []byte(`"primary_wanted":false`), []byte(`"primary_wanted":false,"Primary_Wanted":true`), 1),
		bytes.Replace(raw, []byte(`"members":["b3b3b3b3"]`), []byte(`"members":["b3b3b3b3","b3b3b3b3"]`), 1),
		bytes.Replace(raw, []byte(`"members":["b3b3b3b3"]`), []byte(`"members":["a2a2a2a2"]`), 1),
		bytes.Replace(raw, []byte(`"members":["b3b3b3b3"]`), []byte(`"members":null`), 1),
		append(bytes.Clone(raw), []byte(` {}`)...),
	} {
		before := bytes.Clone(bad)
		if _, err := DecodeAutomaticRestore(bad); err == nil || !bytes.Equal(before, bad) {
			t.Fatal("invalid automatic plan was accepted or changed")
		}
	}
	m := autoRestoreFixture("")
	m.AutomaticRestore = bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1)
	encoded, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var again Manifest
	if err := json.Unmarshal(encoded, &again); err != nil || len(again.AutomaticRestore) == 0 {
		t.Fatal("future raw plan disappeared during metadata roundtrip", err)
	}
	if err := ValidateManifestStart(&again); err == nil {
		t.Fatal("unknown future controller intent started")
	}
}

func autoRestoreFixture(parent string) *Manifest {
	id := api.NewSessionID()
	return &Manifest{Schema: 1, ID: id, SandboxOf: parent, Name: "restore", CreatedAt: time.Now().UTC(), Cwd: "/work", Wall: &WallRecord{Level: "process", Driver: "process"}, Agent: AgentInfo{Kind: "custom", Argv: []string{"/bin/sh", "-c", "exit 0"}, Command: []string{"/bin/sh", "-c", "exit 0"}}, Restart: Restart{Policy: "resume"}, Run: Runtime{Generation: "old-run", BootID: "old-boot"}, Life: &Life{Phase: LifeEnded, End: &LifeEnd{Kind: EndSystem}}, Status: Status{State: StateWorking}}
}
