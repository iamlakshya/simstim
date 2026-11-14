package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func sharedPolicyEngine(t *testing.T, old *Engine, pol api.Policy) *Engine {
	t.Helper()
	e, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}
	if old != nil {
		old.ledger.syncPolicy(e)
	}
	return e
}

func TestOnceRegrantAndPermanentUpgradeKeepInstanceIsolation(t *testing.T) {
	for _, change := range []string{"grant_id", "until", "remove_readd", "permanent"} {
		t.Run(change, func(t *testing.T) {
			rule := api.Rule{Allow: true, Once: true, Target: "example.com", GrantID: "11111111111111111111111111111111", Until: time.Now().Add(time.Hour)}
			if change == "remove_readd" {
				rule.GrantID = ""
			}
			pol := onceTestPolicy(t, api.PresetLocked, rule)
			e := sharedPolicyEngine(t, nil, pol)
			req := Request{Host: "example.com", Port: 443}
			old := e.NewPermit()
			if !e.DecideWithPermit(req, old).Allow {
				t.Fatal("the initial flow was denied")
			}
			old.Commit()
			next := pol
			next.Rules = slices.Clone(pol.Rules)
			switch change {
			case "grant_id":
				next.Rules[0].GrantID = "22222222222222222222222222222222"
			case "until":
				next.Rules[0].Until = rule.Until.Add(time.Hour)
			case "remove_readd":
				e = sharedPolicyEngine(t, e, api.Policy{Preset: api.PresetLocked})
			case "permanent":
				next.Rules[0].Once = false
			}
			e = sharedPolicyEngine(t, e, next)
			if got := e.Revalidate(req, old).Allow; got != (change == "permanent") {
				t.Fatalf("old flow validity after %s = %v", change, got)
			}
			fresh := e.NewPermit()
			defer fresh.Close()
			if !e.DecideWithPermit(req, fresh).Allow {
				t.Fatal("replacement grant could not admit a fresh flow")
			}
			ended := old.Close()
			if len(ended) != 1 || ended[0].GrantID != rule.GrantID || !ended[0].Until.Equal(rule.Until) {
				t.Fatalf("old cleanup lost its exact lease identity: %+v", ended)
			}
			if !e.Revalidate(req, fresh).Allow {
				t.Fatal("old cleanup invalidated the replacement flow")
			}
			fresh.Commit()
			fresh.Close()
			if got := e.Decide(req).Allow; got != (change == "permanent") {
				t.Fatalf("replacement budget after its flow ended = %v", got)
			}
		})
	}
}

func TestOncePermanentAllowWinsWithoutSpendingOverlap(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		rules := []api.Rule{
			{Layer: api.LayerSession, Allow: true, Target: "*.example.com", Once: true},
			{Layer: api.LayerSession, Allow: true, Target: "api.example.com"},
		}
		if reverse {
			slices.Reverse(rules)
		}
		e := sharedPolicyEngine(t, nil, api.Policy{Preset: api.PresetLocked, Rules: rules})
		if !e.Decide(Request{Host: "api.example.com", Port: 443}).Allow {
			t.Fatal("the permanent overlap was denied")
		}
		other := Request{Host: "other.example.com", Port: 443}
		if !e.Decide(other).Allow || e.Decide(other).Allow {
			t.Fatal("the permanent overlap spent the once grant, or the grant was reused")
		}
	}
}

func TestOnceDenyWinsBeforeAnAllowCanBeSpent(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		rules := []api.Rule{
			{Layer: api.LayerSession, Allow: true, Target: "*.example.com", Once: true},
			{Layer: api.LayerSession, Allow: false, Target: "blocked.example.com"},
		}
		if reverse {
			slices.Reverse(rules)
		}
		e := sharedPolicyEngine(t, nil, api.Policy{Preset: api.PresetLocked, Rules: rules})
		if e.Decide(Request{Host: "blocked.example.com", Port: 443}).Allow {
			t.Fatal("a same-layer allow beat the deny")
		}
		other := Request{Host: "other.example.com", Port: 443}
		if !e.Decide(other).Allow || e.Decide(other).Allow {
			t.Fatal("the denied request spent the once grant, or the grant was reused")
		}
	}
}

func TestOnceNumericPrecheckAndPublicAddressDoNotSpend(t *testing.T) {
	pub := netip.MustParseAddr("203.0.113.10")
	e := sharedPolicyEngine(t, nil, onceTestPolicy(t, api.PresetOpen,
		api.Rule{Allow: true, Target: "203.0.113.0/24:80", Once: true},
	))
	for range 4 {
		if !e.PrecheckAddr(pub, 80, nil) {
			t.Fatal("read-only availability consumed the grant")
		}
	}
	d := &Dialer{}
	if _, ok := d.CheckAddr(pub, 80, e.AllowsAddr); !ok {
		t.Fatal("a public address was refused")
	}
	if !e.AllowsAddr(pub, 80) || e.AllowsAddr(pub, 80) {
		t.Fatal("public address checking spent the numeric grant, or standalone consumption was bypassed")
	}
}

func TestOnceLegacyDialConsumesOnlyOneNumericAuthorisation(t *testing.T) {
	addr := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	e := sharedPolicyEngine(t, nil, onceTestPolicy(t, api.PresetOpen,
		api.Rule{Allow: true, Target: addr.String() + ":80", Once: true},
	))
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{addr}, nil },
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			c, peer := net.Pipe()
			_ = peer.Close()
			return c, nil
		},
	}
	conn, _, _, err := d.Dial(context.Background(), "example.com", 80, e.AllowsAddr)
	if err != nil {
		t.Fatalf("legacy Dial re-consumed the first grant: %v", err)
	}
	_ = conn.Close()
	if conn, _, _, err := d.Dial(context.Background(), "example.com", 80, e.AllowsAddr); !errors.Is(err, ErrRefusedAddr) {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("a second legacy Dial reused the grant: %v", err)
	}
}
