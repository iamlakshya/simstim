package egress

import (
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func TestJudgeMetadataOwnsAdmissionProjectionAndRegistryCopies(t *testing.T) {
	want := classify.Metadata{RepositoryRemote: "https://example.com/team/repository", Ecosystems: []string{"go", "npm"}, Agent: "shell", Profile: "review", Task: "inspect the change"}
	input := InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, JudgeMetadata: want}
	prepared, err := prepareInitialState(input)
	if err != nil {
		t.Fatal(err)
	}
	input.JudgeMetadata.Ecosystems[0] = "caller-mutation"
	want.Ecosystems = []string{"go", "npm"}
	if !reflect.DeepEqual(prepared.state.metadata.JudgeMetadata, want) {
		t.Fatal("admission retained caller-owned judge metadata")
	}
	projected, err := prepared.state.initialArgs()
	if err != nil {
		t.Fatal(err)
	}
	projected.JudgeMetadata.Ecosystems[1] = "projection-mutation"
	if !reflect.DeepEqual(prepared.state.metadata.JudgeMetadata, want) {
		t.Fatal("legacy state projection exposed registry metadata storage")
	}
	cloned, err := cloneMemberState(prepared.state)
	if err != nil {
		t.Fatal(err)
	}
	cloned.metadata.JudgeMetadata.Ecosystems[0] = "clone-mutation"
	cloned.metadata.JudgeMetadata.Task = "other task"
	if !reflect.DeepEqual(prepared.state.metadata.JudgeMetadata, want) {
		t.Fatal("registry clone exposed original trusted judge metadata")
	}
}
