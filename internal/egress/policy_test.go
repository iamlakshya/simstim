package egress

import (
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestParsePolicyTOML(t *testing.T) {
	raw := `
session = "devbox/auth-fix"
agent = "claude"

[sandbox]
preset = "balanced"

[network]
allow = ["api.stripe.com", "*.internal.example:8443", "example.com:80"]
deny = ["pastebin.com", "registry.npmjs.org"]
unknown = "deny"

[lock]
network.deny = ["evil.example"]
`
	cfg, err := ParsePolicy([]byte(raw))
	if err != nil {
		t.Fatalf("ParsePolicy failed: %v", err)
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	// Any deny wins over preset and allow.
	if dec := eng.Decide(Request{Host: "registry.npmjs.org", Port: 443}); dec.Action != ActionDeny {
		t.Errorf("registry.npmjs.org should be denied by explicit deny over balanced preset: %+v", dec)
	}
	if dec := eng.Decide(Request{Host: "pastebin.com", Port: 80}); dec.Action != ActionDeny {
		t.Errorf("pastebin.com:80 should be denied by port-less deny: %+v", dec)
	}
	if dec := eng.Decide(Request{Host: "evil.example", Port: 443}); dec.Action != ActionDeny || !strings.HasPrefix(dec.Rule, "lock:") {
		t.Errorf("evil.example should be denied by lock: %+v", dec)
	}

	// Explicit allows, wildcards and ports.
	if dec := eng.Decide(Request{Host: "api.stripe.com", Port: 443}); dec.Action != ActionAllow {
		t.Errorf("api.stripe.com:443 = %+v, want allow", dec)
	}
	if dec := eng.Decide(Request{Host: "api.stripe.com", Port: 80}); dec.Action != ActionDeny {
		t.Errorf("api.stripe.com:80 = %+v, want deny (default allow port is 443)", dec)
	}
	if dec := eng.Decide(Request{Host: "a.b.internal.example", Port: 8443}); dec.Action != ActionAllow {
		t.Errorf("a.b.internal.example:8443 = %+v, want allow", dec)
	}
	if dec := eng.Decide(Request{Host: "internal.example", Port: 8443}); dec.Action != ActionDeny {
		t.Errorf("wildcard apex internal.example should not match *.internal.example: %+v", dec)
	}
	if dec := eng.Decide(Request{Host: "notinternal.example", Port: 8443}); dec.Action != ActionDeny {
		t.Errorf("notinternal.example should not match *.internal.example: %+v", dec)
	}
	if dec := eng.Decide(Request{Host: "example.com", Port: 80}); dec.Action != ActionAllow {
		t.Errorf("example.com:80 = %+v, want allow", dec)
	}
	// Agent and balanced preset hosts pass.
	if dec := eng.Decide(Request{Host: "api.anthropic.com", Port: 443}); dec.Action != ActionAllow {
		t.Errorf("api.anthropic.com:443 = %+v, want allow", dec)
	}
	if dec := eng.Decide(Request{Host: "pypi.org", Port: 443}); dec.Action != ActionAllow {
		t.Errorf("pypi.org:443 = %+v, want allow", dec)
	}
}

func TestParsePolicyRejectsUnknownKeyAndBadTOML(t *testing.T) {
	if _, err := ParsePolicy([]byte("not valid = [toml")); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Errorf("bad TOML err = %v, want error with Try: hint", err)
	}
	if _, err := ParsePolicy([]byte("[network]\nunknown_key = true\n")); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Errorf("unknown key err = %v, want error with Try: hint", err)
	}
}

func TestLockedPresetAgentEgress(t *testing.T) {
	for _, tc := range []struct {
		agent   string
		allowed []string
		denied  []string
	}{
		{
			agent:   "claude",
			allowed: []string{"api.anthropic.com", "platform.claude.com", "claude.ai", "claude.com", "sso.claude.com"},
			denied:  []string{"mcp-proxy.anthropic.com", "downloads.claude.ai", "registry.npmjs.org", "example.com"},
		},
		{
			agent:   "codex",
			allowed: []string{"api.openai.com", "chatgpt.com", "auth.openai.com"},
			denied:  []string{"registry.npmjs.org", "example.com", "api.anthropic.com"},
		},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			eng, err := NewEngine(Config{
				Session: "box/s1",
				Agent:   tc.agent,
				Sandbox: SandboxConfig{Preset: PresetLocked},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range tc.allowed {
				if dec := eng.Decide(Request{Host: h, Port: 443}); dec.Action != ActionAllow {
					t.Errorf("%s locked: %s:443 = %+v, want allow", tc.agent, h, dec)
				}
				if dec := eng.Decide(Request{Host: h, Port: 80}); dec.Action != ActionDeny {
					t.Errorf("%s locked: %s:80 = %+v, want deny", tc.agent, h, dec)
				}
			}
			for _, h := range tc.denied {
				dec := eng.Decide(Request{Host: h, Port: 443})
				if dec.Action != ActionDeny {
					t.Errorf("%s locked: %s:443 = %+v, want deny", tc.agent, h, dec)
				}
				wantHint := brand.Name + " policy allow " + h + " --in box/s1"
				if !strings.Contains(dec.Why, wantHint) {
					t.Errorf("deny hint = %q, want substring %q", dec.Why, wantHint)
				}
			}
		})
	}
}

func TestDenyWinsAndLocksHoldOnLiveGrantsAndReload(t *testing.T) {
	eng, err := NewEngine(Config{
		Session: "dev/s1",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
		Network: NetworkConfig{
			Allow: []string{"both.example"},
			Deny:  []string{"both.example"},
		},
		Lock: LockConfig{
			Deny: []string{"locked.example", "*.locked-wild.example"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Any deny beats any allow in the same config.
	if dec := eng.Decide(Request{Host: "both.example", Port: 443}); dec.Action != ActionDeny {
		t.Fatalf("deny must beat allow: %+v", dec)
	}
	// Live Allow succeeds on unknown host.
	if err := eng.Allow("new.example"); err != nil {
		t.Fatalf("Allow(new.example): %v", err)
	}
	if dec := eng.Decide(Request{Host: "new.example", Port: 443}); dec.Action != ActionAllow {
		t.Fatalf("new.example after live Allow = %+v, want allow", dec)
	}
	// Live Deny beats live Allow.
	if err := eng.Deny("new.example"); err != nil {
		t.Fatalf("Deny(new.example): %v", err)
	}
	if dec := eng.Decide(Request{Host: "new.example", Port: 443}); dec.Action != ActionDeny {
		t.Fatalf("new.example after live Deny = %+v, want deny", dec)
	}
	// Locked host rejects live Allow with a Try: hint.
	if err := eng.Allow("locked.example"); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("Allow(locked.example) = %v, want locked error with Try:", err)
	}
	if err := eng.Allow("sub.locked-wild.example"); err == nil {
		t.Fatal("Allow(sub.locked-wild.example) succeeded despite wildcard lock")
	}
	// Reload preserves existing locks even if the new config omits them.
	if err := eng.Reload(Config{
		Sandbox: SandboxConfig{Preset: PresetOpen},
		Network: NetworkConfig{Allow: []string{"locked.example"}},
	}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if dec := eng.Decide(Request{Host: "locked.example", Port: 443}); dec.Action != ActionDeny {
		t.Fatalf("lock must survive Reload: %+v", dec)
	}
	if dec := eng.Decide(Request{Host: "anything.example", Port: 8443}); dec.Action != ActionAllow {
		t.Fatalf("open preset after Reload = %+v, want allow", dec)
	}
}

func TestPublicSuffixWildcardRejectedInAllow(t *testing.T) {
	for _, bad := range []string{"*.com", "*.co.uk", "*"} {
		if _, err := NewEngine(Config{Network: NetworkConfig{Allow: []string{bad}}}); err == nil {
			t.Errorf("allow %q unexpectedly accepted", bad)
		}
	}
	// Wildcard on a registrable domain is accepted.
	if _, err := NewEngine(Config{Network: NetworkConfig{Allow: []string{"*.example.com"}}}); err != nil {
		t.Errorf("allow *.example.com failed: %v", err)
	}
}

func TestExplicitIPAndShortCIDRGrants(t *testing.T) {
	eng, err := NewEngine(Config{
		Sandbox: SandboxConfig{Preset: PresetLocked},
		Network: NetworkConfig{
			Allow: []string{"10/8:5432", "[2001:db8::1]:8443"},
			Deny:  []string{"10/24:5432"},
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	allowedIP := netip.AddrFrom4([4]byte{10, 1, 2, 3})
	deniedIP := netip.AddrFrom4([4]byte{10, 0, 0, 5})
	v6IP := netip.MustParseAddr("2001:db8::1")

	if !eng.AllowExplicitIP(allowedIP, 5432) {
		t.Errorf("AllowExplicitIP(%s, 5432) = false, want true", allowedIP)
	}
	if eng.AllowExplicitIP(allowedIP, 443) {
		t.Errorf("AllowExplicitIP(%s, 443) = true, want false (wrong port)", allowedIP)
	}
	if eng.AllowExplicitIP(deniedIP, 5432) {
		t.Errorf("AllowExplicitIP(%s, 5432) = true, want false (deny wins)", deniedIP)
	}
	if !eng.AllowExplicitIP(v6IP, 8443) {
		t.Errorf("AllowExplicitIP(%s, 8443) = false, want true", v6IP)
	}
}

func TestEngineConcurrentAccess(t *testing.T) {
	eng, err := NewEngine(Config{Sandbox: SandboxConfig{Preset: PresetBalanced}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = eng.Decide(Request{Host: "registry.npmjs.org", Port: 443})
				_ = eng.DecideDNS("example.com")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			_ = eng.Allow("extra.example")
			_ = eng.Deny("blocked.example")
		}
	}()
	wg.Wait()
}
