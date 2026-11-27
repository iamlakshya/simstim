package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPolicyCompletionRetainsDistinctRulesWithTheSameShortID(t *testing.T) {
	// Concrete legal30-bit API ID collision, generated administratively
	// from the pinned identity formula; actual API verifies the fixture.
	targets := []string{"collision-6295.example", "collision-6343.example"}
	first := api.Rule{Layer: api.LayerAll, Door: api.DoorNet, Allow: true, Target: targets[0]}
	second := first
	second.Target = targets[1]
	if first.ID = api.RuleID(first); first.ID != api.RuleID(second) {
		t.Fatal("fixture no longer collides under actual canonical API")
	}
	settings := DefaultSettings()
	settings.Network.Allow = targets
	metadata, err := CollectPolicyCompletionMetadata(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Rules) != 2 || metadata.Rules[0].Target == metadata.Rules[1].Target || metadata.Rules[0].ID != metadata.Rules[1].ID {
		t.Fatal("distinct configured rows disappeared behind a short ID", metadata.Rules)
	}
	settings.Network.Allow = append(settings.Network.Allow, targets[0])
	metadata, err = CollectPolicyCompletionMetadata(settings, nil)
	if err != nil || len(metadata.Rules) != 2 {
		t.Fatal("exact duplicate identity was not compacted", err)
	}
}
