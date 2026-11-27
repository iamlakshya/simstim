package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRewindPreparationPinsSelectionFloorImageAndCompleteCohort(t *testing.T) {
	selection := RewindArgs{ID: "child234", Checkpoint: "20260203T040506.000Z"}
	snapshot := &SandboxSnapshot{Version: 1, Primary: "parent23", Generation: strings.Repeat("a", 64), Members: []string{"child234"}}
	start := ResumeArgs{ID: selection.ID, Snapshot: snapshot, ExpectedImageV1: json.RawMessage(`{"v":1}`), Secrets: map[string]Secret{"PRIVATE": "value"}, Members: []MemberResumeArgs{{ID: "child234", Secrets: map[string]Secret{"CHILD": "value"}}}, NamedSSHKeys: &NamedSSHKeysV1{Version: 1, Names: []string{"KEY"}, Values: map[string]Secret{"KEY": "value"}}, Prepared: &PreparedResumeV1{V: 1, Token: strings.Repeat("b", 64)}}
	metadata, err := RewindPreparationMetadataV1(selection, start, "microvm")
	if err != nil || metadata.Validate() != nil || metadata.Start.Prepared != nil || metadata.Start.Secrets != nil || metadata.Start.Members != nil || metadata.Start.NamedSSHKeys != nil {
		t.Fatalf("private resume data entered rewind metadata: %v", err)
	}
	digest, err := RewindPreparationDigestV1(selection, start, "microvm")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*RewindArgs, *ResumeArgs, *string){
		func(s *RewindArgs, _ *ResumeArgs, _ *string) { s.Checkpoint = "20260203T040507.000Z" },
		func(_ *RewindArgs, a *ResumeArgs, _ *string) { a.ExpectedImageV1 = json.RawMessage(`{"v":2}`) },
		func(_ *RewindArgs, a *ResumeArgs, _ *string) {
			a.Snapshot = &SandboxSnapshot{Version: 1, Primary: "parent23", Generation: strings.Repeat("a", 64), Members: []string{}}
		},
		func(_ *RewindArgs, _ *ResumeArgs, floor *string) { *floor = "gvisor" },
	} {
		selected, changed, floor := selection, start, "microvm"
		change(&selected, &changed, &floor)
		other, err := RewindPreparationDigestV1(selected, changed, floor)
		if err == nil && other == digest {
			t.Fatal("rewind digest did not pin its exact selection, image, cohort or floor")
		}
	}
	result := RewindPrepareResultV1{V: 1, Prepared: &PreparedRewindV1{V: 1, Token: strings.Repeat("c", 64)}, Selection: selection, Source: snapshot.Clone(), StartDigest: digest}
	if result.Validate(selection, digest, snapshot) != nil {
		t.Fatal("valid descriptive reply was refused")
	}
	result.Source.Members = []string{}
	if result.Validate(selection, digest, snapshot) == nil {
		t.Fatal("a reply enlarged or changed the locally authorised cohort")
	}
	for _, value := range []any{result, result.Prepared} {
		if fmt.Sprintf("%+v", value) != Redacted {
			t.Fatal("rewind token entered diagnostic output")
		}
	}
}

func TestRewindWireRefusesResumeTokenPrivatePrepareAndAliasedSelectors(t *testing.T) {
	selection := RewindArgs{ID: "parent23", Checkpoint: "20260203T040506.000Z"}
	snapshot := &SandboxSnapshot{Version: 1, Primary: selection.ID, Generation: strings.Repeat("a", 64), Members: []string{}}
	start := ResumeArgs{ID: selection.ID, Snapshot: snapshot}
	metadata, err := RewindPreparationMetadataV1(selection, start, "microvm")
	if err != nil {
		t.Fatal(err)
	}
	metadata.Start.Secrets = map[string]Secret{"KEY": "private"}
	if metadata.Validate() == nil {
		t.Fatal("prepare accepted a private value carrier")
	}
	commit := RewindCommitArgsV1{V: 1, Prepared: &PreparedRewindV1{V: 1, Token: strings.Repeat("c", 64)}, Selection: selection, Start: start}
	commit.Start.Prepared = &PreparedResumeV1{V: 1, Token: strings.Repeat("b", 64)}
	if commit.Validate() == nil {
		t.Fatal("an ordinary resume token substituted for rewind's original source")
	}
	for _, raw := range []string{
		`{"v":1,"prepared":{"v":1,"token":"` + strings.Repeat("c", 64) + `"},"selection":{"id":"parent23","id":"child234","checkpoint":"20260203T040506.000Z"}}`,
		`{"v":1,"Prepared":{"v":1,"token":"` + strings.Repeat("c", 64) + `"},"selection":{"id":"parent23","checkpoint":"20260203T040506.000Z"}}`,
	} {
		var args RewindPreparedArgsV1
		if json.Unmarshal([]byte(raw), &args) == nil {
			t.Fatal("ambiguous prepared rewind selector was accepted")
		}
	}
}
