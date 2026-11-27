package egress

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestDecisionMetadataSeparatesMatchedRulesFromPresetFallback(t *testing.T) {
	for _, tc := range []struct {
		name, agent, host, rule string
		preset                  api.Preset
		allow, unmatched        bool
	}{
		{"locked fallback", "shell", "example.com", "preset:locked", api.PresetLocked, false, true},
		{"balanced fallback", "shell", "example.com", "preset:balanced", api.PresetBalanced, false, true},
		{"balanced matched rule", "shell", "registry.npmjs.org", "preset:balanced", api.PresetBalanced, true, false},
		{"open hostname fallback", "shell", "example.com", "preset:open", api.PresetOpen, true, true},
		{"open numeric fallback", "shell", "203.0.113.7", "preset:open", api.PresetOpen, false, true},
		{"agent matched rule", "claude", "api.anthropic.com", "agent:claude", api.PresetLocked, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := NewEngine(tc.agent, api.Policy{Preset: tc.preset})
			if err != nil {
				t.Fatal(err)
			}
			got := eng.evaluate(Request{Host: tc.host, Port: 443}, nil, evalPrecheck)
			if got.Allow != tc.allow || got.Rule != tc.rule || got.unmatched != tc.unmatched {
				t.Fatalf("decision = %+v, want allow=%v rule=%q unmatched=%v", got, tc.allow, tc.rule, tc.unmatched)
			}
		})
	}
}

func TestDecisionMetadataDoesNotTrustCollidingRuleLabels(t *testing.T) {
	for _, label := range []string{"preset:locked", "preset:balanced", "preset:open", "invalid"} {
		t.Run(label, func(t *testing.T) {
			eng, err := NewEngine("shell", api.Policy{Preset: api.PresetOpen, Rules: []api.Rule{
				{ID: label, Layer: api.LayerSession, Door: api.DoorNet, Target: "example.com"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			got := eng.evaluate(Request{Host: "example.com", Port: 443}, nil, evalPrecheck)
			if got.Allow || got.unmatched || got.Rule != "session:"+label {
				t.Fatalf("opaque configured label altered matched denial: %+v", got)
			}
			// Force an exact diagnostic collision at the compiled rule
			// boundary too. Only the control-flow branch can set metadata.
			deny, err := compileTarget("example.com", false)
			if err != nil {
				t.Fatal(err)
			}
			deny.id = label
			eng.locks = []compiledRule{deny}
			got = eng.evaluate(Request{Host: "example.com", Port: 443}, nil, evalPrecheck)
			if got.Allow || got.unmatched || got.Rule != label {
				t.Fatalf("colliding diagnostic label became fallback authority: %+v", got)
			}
		})
	}
}

func TestDecisionMetadataPreservesOnceAndExpirySemantics(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	pol := pathTestPolicy(t, api.PresetLocked, "example.com")
	pol.Rules[0].Once, pol.Rules[0].GrantID = true, "metadata-grant"
	eng, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}
	eng.now = func() time.Time { return now }
	req := Request{Host: "example.com", Port: 443}
	for range 2 {
		if got := eng.evaluate(req, nil, evalPrecheck); !got.Allow || got.unmatched {
			t.Fatalf("read-only check consumed or lost an eligible rule: %+v", got)
		}
	}
	permit := eng.NewPermit()
	if got := eng.DecideWithPermit(req, permit); !got.Allow || got.unmatched {
		t.Fatalf("first reservation was not a matched allow: %+v", got)
	}
	if got := eng.evaluate(req, nil, evalPrecheck); got.Allow || !got.unmatched {
		t.Fatalf("reserved grant did not retain its preset fallback semantics: %+v", got)
	}
	if got := eng.Revalidate(req, permit); !got.Allow || got.unmatched {
		t.Fatalf("owner lost its existing matched grant: %+v", got)
	}
	permit.Close()
	if got := eng.Decide(req); !got.Allow || got.unmatched {
		t.Fatalf("released grant was not available to its first standalone use: %+v", got)
	}
	if got := eng.evaluate(req, nil, evalPrecheck); got.Allow || !got.unmatched {
		t.Fatalf("spent grant was rearmed or misclassified: %+v", got)
	}
	pol.Rules[0].Once = false
	pol.Rules[0].Until = now
	expired, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}
	if got := expired.evaluate(Request{Host: "example.com", Port: 443, Now: now}, nil, evalPrecheck); got.Allow || !got.unmatched {
		t.Fatalf("expired rule altered its original fallback semantics: %+v", got)
	}
	if got := eng.evaluate(Request{Host: "invalid host", Port: 443}, nil, evalPrecheck); got.Allow || got.unmatched {
		t.Fatalf("invalid input was classified as an unmatched host: %+v", got)
	}
}
