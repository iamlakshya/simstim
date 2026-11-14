package egress

import (
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nettarget"
)

func TestClientAndEngineUseOneCanonicalTargetParser(t *testing.T) {
	for _, raw := range []string{
		"EXAMPLE.COM.", "*.example.com:8443/login%2F", "example.com:80/123",
		"example.com/my%20file", "example.com/caf%c3%a9", "203.0.113/24",
		"0.0.0.0/0", "::ffff:203.0.113.7", "[2001:db8::/32]:443",
		"localhost", "service.local", "localhost.example.com",
	} {
		clientName, _, err := nettarget.Classify(raw)
		if err != nil {
			t.Fatal(err)
		}
		engineName, err := ValidateRuleTarget(raw)
		if err != nil || engineName != clientName {
			t.Fatalf("client and engine disagree on %q: client=%q engine=%q error=%v", raw, clientName, engineName, err)
		}
		deny, err := compileTarget(raw, false)
		if err != nil || formatCompiledTarget(deny) != clientName {
			t.Fatalf("deny syntax changed during parser extraction: %+v, %v", deny, err)
		}
	}
}

func TestCapabilityClassificationDoesNotRelaxRuntimeHostAddressRefusal(t *testing.T) {
	const raw = "203.0.113.7"
	_, private, err := nettarget.Classify(raw)
	if err != nil || !private {
		t.Fatalf("an explicit numeric exception was not an own-only capability: %v", err)
	}
	addr := netip.MustParseAddr(raw)
	d := &Dialer{HostAddrs: []netip.Addr{addr}}
	if rule, ok := d.CheckAddr(addr, 443, nil); ok || rule != "range:host" {
		t.Fatalf("classification altered the host-interface refusal: %q %v", rule, ok)
	}
	eng, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: raw},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.CheckAddr(addr, 443, eng.AllowsAddr); !ok {
		t.Fatal("a numeric capability became a parser or runtime ban for an own-tier caller")
	}
}
