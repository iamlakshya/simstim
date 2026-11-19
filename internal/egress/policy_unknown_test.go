package egress

import (
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestConfiguredUnknownOverridesOnlyTypedUnmatchedOpenFallback(t *testing.T) {
	for _, tc := range []struct {
		action api.Unknown
		allow  bool
		rule   string
	}{
		{"", true, "preset:open"},
		{api.UnknownAsk, false, "unknown:ask"},
		{api.UnknownDeny, false, "unknown:deny"},
		{api.UnknownAllow, true, "unknown:allow"},
		{api.UnknownClassify, false, "classify:unsure"},
	} {
		engine, err := NewEngine("shell", api.Policy{Preset: api.PresetOpen, Unknown: tc.action})
		if err != nil {
			t.Fatal(err)
		}
		before := snapshotDNSLedger(engine)
		got := engine.Decide(Request{Host: "authored.example", Port: 443})
		if got.Allow != tc.allow || got.Rule != tc.rule || !got.unmatched || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
			t.Fatal("configured unknown action did not decide the actual unmatched fallback")
		}
		if tc.action == "" && engine.Decide(Request{Host: "1.1.1.1", Port: 443}).Allow {
			t.Fatal("legacy omitted-unknown Open acquired unnamed-IP permission")
		}
	}
}

func TestConfiguredUnknownUsesActualValidatedScalarPresence(t *testing.T) {
	for _, action := range []string{"ask", "deny", "allow", "classify"} {
		policy := api.Policy{Preset: api.PresetOpen, LayerValues: api.LayerValues{Network: api.NetworkValues{Unknown: &action}}}
		engine, err := NewEngine("shell", policy)
		if err != nil || engine.unknown != api.Unknown(action) || policy.Unknown != "" || *policy.Network.Unknown != action {
			t.Fatal("structured presence was inferred, lost or mutated", err)
		}
	}
	for _, policy := range []api.Policy{
		{Preset: api.PresetOpen, Unknown: api.UnknownDeny, LayerValues: api.LayerValues{Network: api.NetworkValues{Unknown: new("allow")}}},
		{Preset: api.PresetOpen, LayerValues: api.LayerValues{Network: api.NetworkValues{Unknown: new("")}}},
		{Preset: api.PresetOpen, LayerValues: api.LayerValues{Network: api.NetworkValues{Unknown: new("future")}}},
	} {
		if _, err := NewEngine("shell", policy); err == nil {
			t.Fatal("conflicting or invalid present setting acquired fallback permission")
		}
	}
	merged, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Preset: api.PresetOpen}})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine("shell", merged)
	if err != nil || engine.unknown != api.UnknownAsk || engine.Decide(Request{Host: "authored.example", Port: 443}).Allow {
		t.Fatal("the real merged builtin ask was treated as an omitted legacy value", err)
	}
}

func TestConfiguredUnknownKeepsActualNamedAgentPresetAndLockDecisions(t *testing.T) {
	for _, tc := range []struct {
		agent, host string
		preset      api.Preset
		rules       []api.Rule
	}{
		{"shell", "named.example", api.PresetOpen, []api.Rule{{Layer: api.LayerSession, Target: "named.example", Allow: true}}},
		{"claude", "api.anthropic.com", api.PresetOpen, nil},
		{"shell", "registry.npmjs.org", api.PresetBalanced, nil},
	} {
		engine, err := NewEngine(tc.agent, api.Policy{Preset: tc.preset, Unknown: api.UnknownDeny, Rules: tc.rules})
		if err != nil {
			t.Fatal(err)
		}
		got := engine.Decide(Request{Host: tc.host, Port: 443})
		if !got.Allow || got.unmatched || got.Match == nil {
			t.Fatal("an unknown action overrode a real named source match")
		}
	}
	for _, lock := range []bool{false, true} {
		policy := api.Policy{Preset: api.PresetOpen, Unknown: api.UnknownAllow}
		if lock {
			policy.Locks = []api.Lock{{Layer: api.LayerAll, Deny: []string{"authored.example"}}}
		} else {
			policy.Rules = []api.Rule{{Layer: api.LayerSession, ID: "unknown:allow", Target: "authored.example"}}
		}
		engine, err := NewEngine("shell", policy)
		if err != nil {
			t.Fatal(err)
		}
		if got := engine.Decide(Request{Host: "authored.example", Port: 443}); got.Allow || got.unmatched {
			t.Fatal("a diagnostic label or unknown allowance lifted an actual denial")
		}
	}
}

func TestConfiguredUnknownLiveReplacementRevalidatesOriginalPermit(t *testing.T) {
	old, err := NewEngine("shell", api.Policy{Preset: api.PresetOpen, Unknown: api.UnknownAllow})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: "authored.example", Port: 443}
	permit := old.NewPermit()
	defer permit.Close()
	if !old.DecideWithPermit(request, permit).Allow {
		t.Fatal("live fixture was not initially admitted")
	}
	permit.Commit()
	next, err := NewEngine("shell", api.Policy{Preset: api.PresetOpen, Unknown: api.UnknownDeny})
	if err != nil {
		t.Fatal(err)
	}
	old.ledger.syncPolicy(next)
	var cancelled atomic.Bool
	flows := newFlowTracker(1)
	flows.add(activeFlow{request: request, permit: permit, cancel: func() { cancelled.Store(true) }})
	flows.revalidate(next, new(Dialer))
	if next.Revalidate(request, permit).Allow || !cancelled.Load() {
		t.Fatal("legacy Open replayed permission after a configured deny replacement")
	}
}
