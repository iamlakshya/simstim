package egress

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBalancedPreset(t *testing.T) {
	pol, _, err := MergeLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if pol.Preset != api.PresetBalanced {
		t.Fatalf("default preset = %q, want balanced", pol.Preset)
	}
	eng, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}

	allowed := []struct {
		host string
		port int
	}{
		{"registry.npmjs.org", 443},
		{"registry.yarnpkg.com", 443},
		{"pypi.org", 443},
		{"files.pythonhosted.org", 443},
		{"crates.io", 443},
		{"static.crates.io", 443},
		{"index.crates.io", 443},
		{"proxy.golang.org", 443},
		{"sum.golang.org", 443},
		{"github.com", 443},
		{"api.github.com", 443},
		{"codeload.github.com", 443},
		{"objects.githubusercontent.com", 443},
		{"raw.githubusercontent.com", 443},
		{"release-assets.githubusercontent.com", 443},
		{"gitlab.com", 443},
		{"codeberg.org", 443},
	}
	for _, tc := range allowed {
		dec := eng.Decide(Request{Host: tc.host, Port: tc.port})
		if !dec.Allow || dec.Rule != "preset:balanced" {
			t.Errorf("Decide(%s:%d) = %+v, want allow by preset:balanced", tc.host, tc.port, dec)
		}
	}

	denied := []struct {
		host string
		port int
	}{
		{"example.com", 443},
		{"github.com", 22},
		{"gitlab.com", 22},
		{"codeberg.org", 22},
		{"registry.npmjs.org", 80},
		{"s3.amazonaws.com", 443},
		{"storage.googleapis.com", 443},
		{"r2.cloudflarestorage.com", 443},
	}
	for _, tc := range denied {
		dec := eng.Decide(Request{Host: tc.host, Port: tc.port})
		if dec.Allow {
			t.Errorf("Decide(%s:%d) = %+v, want deny", tc.host, tc.port, dec)
		}
	}
}

func TestLockedPresetAllowsOnlyAgentEgress(t *testing.T) {
	pol := api.Policy{Preset: api.PresetLocked}
	claudeEng, err := NewEngine("claude", pol)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"api.anthropic.com", "platform.claude.com", "claude.ai", "claude.com", "sso.claude.com"} {
		dec := claudeEng.Decide(Request{Host: h, Port: 443})
		if !dec.Allow || dec.Rule != "agent:claude" {
			t.Errorf("claude locked Decide(%s) = %+v, want allow by agent:claude", h, dec)
		}
	}
	for _, h := range []string{"mcp-proxy.anthropic.com", "downloads.claude.ai", "registry.npmjs.org", "example.com"} {
		if dec := claudeEng.Decide(Request{Host: h, Port: 443}); dec.Allow {
			t.Errorf("claude locked Decide(%s) = %+v, want deny", h, dec)
		}
	}

	codexEng, err := NewEngine("codex", pol)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"api.openai.com", "chatgpt.com", "auth.openai.com"} {
		dec := codexEng.Decide(Request{Host: h, Port: 443})
		if !dec.Allow || dec.Rule != "agent:codex" {
			t.Errorf("codex locked Decide(%s) = %+v, want allow by agent:codex", h, dec)
		}
	}
	for _, h := range []string{"api.anthropic.com", "registry.npmjs.org", "example.com"} {
		if dec := codexEng.Decide(Request{Host: h, Port: 443}); dec.Allow {
			t.Errorf("codex locked Decide(%s) = %+v, want deny", h, dec)
		}
	}
}

func TestOpenPresetDeniesUnlistedNumericIP(t *testing.T) {
	eng, err := NewEngine("shell", api.Policy{Preset: api.PresetOpen})
	if err != nil {
		t.Fatal(err)
	}
	if dec := eng.Decide(Request{Host: "example.com", Port: 443}); !dec.Allow {
		t.Errorf("open Decide(example.com:443) = %+v, want allow", dec)
	}
	if dec := eng.Decide(Request{Host: "203.0.113.7", Port: 443}); dec.Allow {
		t.Errorf("open Decide(203.0.113.7:443) = %+v, want deny without explicit IP rule", dec)
	}
}

func TestLayersLocksWildcardsOnceAndExpiry(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	layers := []api.Layer{
		{
			Kind:   api.LayerAll,
			Preset: api.PresetBalanced,
			Rules: []api.Rule{
				{Allow: true, Door: api.DoorNet, Target: "*.example.com"},
			},
			Lock: &api.Lock{
				Deny: []string{"evil.example.com", "*.corp.example"},
			},
		},
		{
			Kind: api.LayerRepo,
			// Repo locks are ignored per spec 8.2.
			Lock: &api.Lock{Preset: api.PresetLocked, Deny: []string{"registry.npmjs.org"}},
		},
		{
			Kind: api.LayerSession,
			Rules: []api.Rule{
				{Allow: false, Door: api.DoorNet, Target: "blocked.example.com"},
				{Allow: true, Door: api.DoorNet, Target: "blocked.example.com"}, // deny in same layer wins
				{Allow: true, Door: api.DoorNet, Target: "once.test", Once: true},
				{Allow: true, Door: api.DoorNet, Target: "temp.test/v1/", Until: t0.Add(5 * time.Minute)},
				{Allow: true, Door: api.DoorNet, Target: "203.0.113.0/24:8443"},
			},
		},
	}
	pol, _, err := MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}

	// Wildcard matches subdomain on 443, not apex and not other ports.
	if !eng.Decide(Request{Host: "sub.example.com", Port: 443, Now: t0}).Allow {
		t.Error("*.example.com did not allow sub.example.com:443")
	}
	if eng.Decide(Request{Host: "example.com", Port: 443, Now: t0}).Allow {
		t.Error("*.example.com allowed apex example.com")
	}
	// Deny beats allow in the same layer.
	if eng.Decide(Request{Host: "blocked.example.com", Port: 443, Now: t0}).Allow {
		t.Error("allow beat deny in session layer")
	}
	// Repo lock was ignored.
	if !eng.Decide(Request{Host: "registry.npmjs.org", Port: 443, Now: t0}).Allow {
		t.Error("repo lock unexpectedly took effect")
	}
	// Once rule allows the first connection and denies the second.
	if !eng.Decide(Request{Host: "once.test", Port: 443, Now: t0}).Allow {
		t.Error("once rule denied first flow")
	}
	if eng.Decide(Request{Host: "once.test", Port: 443, Now: t0}).Allow {
		t.Error("once rule allowed second flow")
	}
	// Path prefix and Until expiry.
	if !eng.Decide(Request{Host: "temp.test", Port: 443, Path: "/v1/models", Now: t0}).Allow {
		t.Error("path prefix rule denied /v1/models before expiry")
	}
	if eng.Decide(Request{Host: "temp.test", Port: 443, Path: "/v2/models", Now: t0}).Allow {
		t.Error("path prefix rule allowed /v2/models")
	}
	if eng.Decide(Request{Host: "temp.test", Port: 443, Path: "/v1/models", Now: t0.Add(6 * time.Minute)}).Allow {
		t.Error("expired Until rule still allowed flow")
	}
	// Numeric CIDR allow opens AllowsAddr only on the matching port.
	if !eng.AllowsAddr(netip.MustParseAddr("203.0.113.9"), 8443) {
		t.Error("AllowsAddr false for 203.0.113.9:8443")
	}
	if eng.AllowsAddr(netip.MustParseAddr("203.0.113.9"), 443) {
		t.Error("AllowsAddr true on wrong port 443")
	}

	// Narrower layer attempting to override a lock fails with policy_locked and Hint.
	_, _, err = MergeLayers([]api.Layer{
		{Kind: api.LayerHost, Lock: &api.Lock{Preset: api.PresetLocked, Deny: []string{"*.corp.example"}}},
		{Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: "api.corp.example"}}},
	})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "policy_locked" || pe.Hint == "" {
		t.Errorf("locked deny override err = %v, want *proto.Error policy_locked with Hint", err)
	}
}

func TestNormaliseHostRejectsTricks(t *testing.T) {
	if got, err := NormaliseHost("API.Anthropic.COM."); err != nil || got != "api.anthropic.com" {
		t.Errorf("NormaliseHost(API.Anthropic.COM.) = %q, %v", got, err)
	}
	bad := []string{
		"",
		".",
		"api..anthropic.com",
		"-bad.example.com",
		"2130706433",
		"0177.0.0.1",
		"0x7f.0.0.1",
		"api.anthropic.com:443",
		"api.anthropic.com/v1",
		"user@example.com",
		"[fe80::1%eth0]",
		"api\x00.example.com",
	}
	for _, in := range bad {
		if got, err := NormaliseHost(in); err == nil {
			t.Errorf("NormaliseHost(%q) = %q, want error", in, got)
		}
	}
	if _, err := ValidateRuleTarget("*"); err == nil {
		t.Error("ValidateRuleTarget(*) succeeded, want error")
	}
}
