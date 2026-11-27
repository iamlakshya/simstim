package egress

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLegacyGroupedRulesKeepExactDiagnosticSourceCopies(t *testing.T) {
	policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerRepo, Name: "legacy-allow", Allow: true, Target: "api.example.com"},
		{Layer: api.LayerRepo, Name: "legacy-deny", Target: "api.example.com"},
	}}
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: "api.example.com", Port: 443}
	first := engine.Decide(request)
	if first.Allow || first.Match == nil || first.Match.Name != "legacy-deny" || first.Match.Kind != api.LayerRepo {
		t.Fatalf("legacy kind grouping lost the exact chosen rule source: %+v", first)
	}
	first.Match.Name = "changed-copy"
	if again := engine.Decide(request); again.Match == nil || again.Match.Name != "legacy-deny" {
		t.Fatalf("a returned diagnostic pointer changed canonical rule state: %+v", again)
	}
}
