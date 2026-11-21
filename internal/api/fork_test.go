package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestForkCheckpointCodecRejectsDuplicateAndAliasedSelections(t *testing.T) {
	for _, raw := range []string{
		`{"id":"parent23","count":1,"id":"another2"}`,
		`{"id":"parent23","Count":1}`,
		`{"id":"parent23","count":1,"extra":true}`,
	} {
		var args ForkArgs
		if json.Unmarshal([]byte(raw), &args) == nil {
			t.Errorf("ambiguous fork request was accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"id":"parent23","checkpoint":"20260203T040506.000Z","Checkpoint":"20260203T040507.000Z"}`,
		`{"id":"parent23","checkpoint":"20260203T040506.000Z","checkpoint":"20260203T040507.000Z"}`,
	} {
		var args RewindArgs
		if json.Unmarshal([]byte(raw), &args) == nil {
			t.Errorf("ambiguous rewind selection was accepted: %s", raw)
		}
	}
	var result CheckpointsResult
	if json.Unmarshal([]byte(`{"id":"parent23","checkpoints":[{"name":"20260203T040506.000Z","name":"20260203T040507.000Z","created_at":"2026-02-03T04:05:06Z"}]}`), &result) == nil {
		t.Fatal("duplicated nested checkpoint name was accepted")
	}
}

func TestForkResultAccountsForIndependentChildAdmission(t *testing.T) {
	args := ForkArgs{ID: "parent23", Count: 3}
	valid := ForkResult{Parent: args.ID, Children: []SessionInfo{{ID: "child234"}, {ID: "child235"}}, Failures: []ForkFailure{{Index: 2, Error: proto.Error{Code: "no_room", Message: "the child does not fit", Hint: "Choose a larger host."}}}}
	if err := valid.Validate(args); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ForkResult){
		func(r *ForkResult) { r.Parent = "another2" },
		func(r *ForkResult) { r.Children[0].ID = args.ID },
		func(r *ForkResult) { r.Children[1].ID = r.Children[0].ID },
		func(r *ForkResult) { r.Failures[0].Index = 0 },
		func(r *ForkResult) { r.Failures[0].Index = 4 },
		func(r *ForkResult) { r.Failures[0].Error.Hint = "" },
		func(r *ForkResult) { r.Failures = nil },
	} {
		candidate := valid
		candidate.Children = slices.Clone(valid.Children)
		candidate.Failures = slices.Clone(valid.Failures)
		change(&candidate)
		if err := candidate.Validate(args); err == nil {
			t.Errorf("accepted invalid fork result: %+v", candidate)
		}
	}
	allFailed := ForkResult{Parent: args.ID, Failures: []ForkFailure{valid.Failures[0], valid.Failures[0], valid.Failures[0]}}
	if err := allFailed.Validate(args); err == nil {
		t.Fatal("repeated failed request slots were accepted")
	}
}

func TestCheckpointListUsesCanonicalImmutableNamesAndNewestFirst(t *testing.T) {
	one := time.Date(2026, 2, 3, 4, 5, 6, 7e6, time.UTC)
	two := one.Add(time.Second)
	ref := proto.BundleRef{Name: "refs/heads/task/child234/work", OID: strings.Repeat("a", 40)}
	result := CheckpointsResult{ID: "child234", Checkpoints: []CheckpointInfo{{Name: two.Format(CheckpointTimeLayout), CreatedAt: two, Refs: []proto.BundleRef{ref}}, {Name: one.Format(CheckpointTimeLayout), CreatedAt: one}}, CurrentRefs: []proto.BundleRef{ref}}
	if err := result.Validate(result.ID); err != nil {
		t.Fatal(err)
	}
	copy := result.Clone()
	copy.Checkpoints[0].Refs[0].OID = strings.Repeat("b", 40)
	copy.CurrentRefs[0].Name = "refs/heads/another"
	if result.Checkpoints[0].Refs[0].OID != ref.OID || result.CurrentRefs[0].Name != ref.Name {
		t.Fatal("display clone changed the retained checkpoint selection")
	}
	for _, name := range []string{"../disk", one.Format(time.RFC3339Nano), "20260203T040506.007Z.disk", "20260230T040506.007Z"} {
		if ValidCheckpointName(name) {
			t.Errorf("accepted checkpoint path or noncanonical time %q", name)
		}
	}
	for _, change := range []func(*CheckpointsResult){
		func(r *CheckpointsResult) { slices.Reverse(r.Checkpoints) },
		func(r *CheckpointsResult) { r.Checkpoints[1] = r.Checkpoints[0] },
		func(r *CheckpointsResult) { r.Checkpoints[0].CreatedAt = one },
		func(r *CheckpointsResult) { r.Checkpoints[0].Refs[0].Name = "refs/heads/a\x1b[2J" },
		func(r *CheckpointsResult) { r.Checkpoints[0].Refs[0].OID = "--option" },
	} {
		candidate := result.Clone()
		change(&candidate)
		if candidate.Validate(result.ID) == nil {
			t.Errorf("accepted malformed checkpoint list: %+v", candidate)
		}
	}
}

func TestCheckpointMetadataAndRewindSelectionAreBounded(t *testing.T) {
	when := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	selected := CheckpointInfo{Name: when.Format(CheckpointTimeLayout), CreatedAt: when}
	request := RewindArgs{ID: "child234", Checkpoint: selected.Name}
	result := RewindResult{ID: request.ID, Checkpoint: request.Checkpoint, Session: SessionInfo{ID: request.ID}}
	if err := result.Validate(request); err != nil {
		t.Fatal(err)
	}
	result.Checkpoint = when.Add(time.Second).Format(CheckpointTimeLayout)
	if result.Validate(request) == nil {
		t.Fatal("a shifted checkpoint selection was accepted")
	}
	result.Checkpoint = request.Checkpoint
	result.Session.ID = "another2"
	if result.Validate(request) == nil {
		t.Fatal("rewind answered for another session")
	}
	selected.Refs = make([]proto.BundleRef, CheckpointRefsMax+1)
	if selected.Validate() == nil {
		t.Fatal("over-limit branch claims were accepted")
	}
	if (CheckpointsResult{ID: request.ID, Checkpoints: make([]CheckpointInfo, CheckpointListMax+1)}).Validate(request.ID) == nil {
		t.Fatal("over-limit checkpoint list was accepted")
	}
	oversize := ForkResult{Parent: "parent23", Children: []SessionInfo{{ID: "child234", Reason: strings.Repeat("a", proto.MaxFrame)}}}
	if oversize.Validate(ForkArgs{ID: "parent23", Count: 1}) == nil {
		t.Fatal("over-frame fork metadata was accepted")
	}
}
