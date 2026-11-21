package egress

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func publicOnlyPolicy(host string, port int, restriction bool, address netip.Addr) api.Policy {
	return api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerSession, Allow: true, Target: net.JoinHostPort(host, portText(port)), PublicOnly: restriction},
		{Layer: api.LayerAll, Allow: true, Target: net.JoinHostPort(address.String(), portText(port))},
	}}
}

func portText(port int) string { return strconv.Itoa(port) }

func TestPublicOnlyPhysicalAddressCeilingBeatsExistingNumericGrants(t *testing.T) {
	public := netip.AddrFrom4([4]byte{203, 0, 113, 9})
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	mappedPublic := netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 203, 14: 113, 15: 9})
	mappedPrivate := netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 10, 13: 23, 15: 1})
	embeddedPrivate := netip.AddrFrom16([16]byte{1: 0x64, 2: 0xff, 3: 0x9b, 12: 10, 13: 23, 15: 1})
	for _, row := range []struct {
		name    string
		address netip.Addr
		own     bool
		allowed bool
	}{
		{"public", public, false, true}, {"mapped-public", mappedPublic, false, true},
		{"private", private, false, false}, {"mapped-private", mappedPrivate, false, false},
		{"embedded-private", embeddedPrivate, false, false},
		{"link-local", netip.AddrFrom4([4]byte{169, 254, 1, 2}), false, false},
		{"shared", netip.AddrFrom4([4]byte{100, 64, 1, 2}), false, false},
		{"own-public", public, true, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			engine, err := NewEngine("shell", publicOnlyPolicy("updates.example", 8443, true, row.address))
			if err != nil {
				t.Fatal(err)
			}
			request := Request{Host: "updates.example", Port: 8443}
			permit := engine.NewPermit()
			defer permit.Close()
			if !engine.DecideWithPermit(request, permit).Allow {
				t.Fatal("named restriction is not itself a refusal")
			}
			dials := 0
			dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{row.address}, nil }, DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
				dials++
				a, b := net.Pipe()
				_ = b.Close()
				return a, nil
			}}
			if row.own {
				dialer.HostAddrs = []netip.Addr{row.address}
			}
			conn, _, _, err := dialer.dialWithPermit(t.Context(), func() *Engine { return engine }, request.Host, request.Port, permit, &request)
			if conn != nil {
				_ = conn.Close()
			}
			if row.allowed {
				if err != nil || dials != 1 {
					t.Fatal("public restricted dial was refused")
				}
			} else if !errors.Is(err, ErrRefusedAddr) || dials != 0 {
				t.Fatal("restricted flow reached a refused physical address through an old numeric grant")
			}
		})
	}
}

func TestPublicOnlyStickyPermitSurvivesPolicyBroadeningAndRebind(t *testing.T) {
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	narrow, err := NewEngine("shell", publicOnlyPolicy("updates.example", 8443, true, private))
	if err != nil {
		t.Fatal(err)
	}
	broad, err := NewEngine("shell", publicOnlyPolicy("updates.example", 8443, false, private))
	if err != nil {
		t.Fatal(err)
	}
	narrow.ledger.syncPolicy(broad)
	request := Request{Host: "updates.example", Port: 8443}
	permit := narrow.NewPermit()
	defer permit.Close()
	if !narrow.DecideWithPermit(request, permit).Allow || !broad.Revalidate(request, permit).Allow {
		t.Fatal("current ordinary policy did not remain otherwise allowed")
	}
	if !broad.AllowsAddr(private, 8443) || broad.RevalidateAddr(private, 8443, permit) {
		t.Fatal("sticky narrowing was lost or legacy numeric exception changed")
	}
	if flowAllowed(broad, &Dialer{}, activeFlow{request: request, addr: private, permit: permit}) {
		t.Fatal("live flow revalidation widened its address ceiling")
	}
	current := broad
	dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		current = broad
		return []netip.Addr{private}, nil
	}, DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
		t.Error("rebound private target reached physical dial")
		return nil, errors.New("unexpected dial")
	}}
	if conn, _, _, err := dialer.dialWithPermit(t.Context(), func() *Engine { return current }, request.Host, request.Port, permit, &request); err == nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal("new DNS answer bypassed sticky ceiling")
	}
	// A separate legacy permit still retains the established numeric API.
	ordinary := broad.NewPermit()
	defer ordinary.Close()
	if !broad.DecideWithPermit(request, ordinary).Allow || !flowAllowed(broad, &Dialer{}, activeFlow{request: request, addr: private, permit: ordinary}) {
		t.Fatal("restriction leaked into an independent old-style grant")
	}
}

func TestPublicOnlyReadOnlyProjectionAndAtomicServicePlan(t *testing.T) {
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	policy := publicOnlyPolicy("api.example.com", 443, true, private)
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: "api.example.com", Port: 443, Method: "GET", Path: "/"}
	if engine.evaluateAddrRequest(private, 443, nil, evalPrecheck, &request) || engine.precheckResolvedAddr(request.Host, private, 443) {
		t.Fatal("read-only DNS witness combined a restricted host with a numeric exception")
	}
	service := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerSession, Allow: true, Target: "api.example.com"},
		{Layer: api.LayerSession, Allow: true, Target: "api.example.com/operation", PublicOnly: true, Once: true, GrantID: "endpoint"},
	}}
	engine, err = NewEngine("shell", service)
	if err != nil {
		t.Fatal(err)
	}
	request.Path = "/operation"
	permit := engine.NewPermit()
	defer permit.Close()
	if !engine.admitServiceRequest(request, permit, true, evalReserve).Allow {
		t.Fatal("same atomic endpoint plan was refused")
	}
	engine.ledger.mu.Lock()
	restricted, owned := permit.publicOnly, len(permit.owned)
	engine.ledger.mu.Unlock()
	if !restricted || owned != 1 {
		t.Fatal("path grant lost its ceiling or spent a separate host lease")
	}
}

func TestPublicOnlyModelFreeCoverageRetainsCeiling(t *testing.T) {
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	policy := api.Policy{Preset: api.PresetOpen, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"mcp"}, PublicOnly: true}, {Layer: api.LayerAll, Allow: true, Target: private.String()}}}
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	permit := engine.NewPermit()
	defer permit.Close()
	request := Request{Host: "api.openai.com", Port: 443, Method: "POST", Path: "/v1/responses"}
	if refusal := engine.admitModelRequest(&modelRequest{request: request, features: []string{"mcp"}}, permit, true, evalReserve); refusal != "" {
		t.Fatal(refusal)
	}
	engine.ledger.mu.Lock()
	restricted, owned := permit.publicOnly, len(permit.owned)
	engine.ledger.mu.Unlock()
	if !restricted || owned != 0 || flowAllowed(engine, &Dialer{}, activeFlow{request: request, addr: private, permit: permit}) {
		t.Fatal("permanent free capability lost its ceiling or invented a once charge")
	}
}

func TestPublicOnlyColdResumeIdentityRemovalAndLocks(t *testing.T) {
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{{Target: "api.example.com", Allow: true, PublicOnly: true}, {Target: "api.example.com", Allow: true}}}}}}
	policy, clean, err := MergeLayers(layers)
	if err != nil || len(policy.Rules) != 2 {
		t.Fatal("address scopes collapsed", err)
	}
	raw, err := json.Marshal(api.CloneLayers(clean))
	if err != nil {
		t.Fatal(err)
	}
	var restored []api.Layer
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	resumed, _, err := MergeLayers(restored)
	if err != nil || len(resumed.Rules) != 2 {
		t.Fatal("cold replay lost distinct address scopes", err)
	}
	restricted := 0
	for _, rule := range resumed.Rules {
		if rule.PublicOnly {
			restricted++
		}
	}
	if restricted != 1 {
		t.Fatal("cold replay widened the restricted grant")
	}
	restrictedRule := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.example.com", PublicOnly: true}
	ordinary := restrictedRule
	ordinary.PublicOnly = false
	if same, err := MatchesRuleReplacement(restrictedRule, ordinary); err != nil || same {
		t.Fatal("ordinary replacement erased a distinct restricted tuple")
	}
	restrictedRule.ID = api.RuleID(ordinary)
	if _, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{restrictedRule}}); err == nil {
		t.Fatal("restricted grant accepted an old identity")
	}
	layers = append(layers, api.Layer{Kind: api.LayerSession, LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{{Target: "api.example.com", Allow: true, PublicOnly: true, Remove: true}}}}})
	remaining, _, err := MergeLayers(layers)
	if err != nil || len(remaining.Rules) != 1 || remaining.Rules[0].PublicOnly {
		t.Fatal("exact restricted removal deleted the ordinary tuple", err)
	}
	if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerAll, Lock: &api.Lock{Deny: []string{"api.example.com"}}}, {Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "api.example.com", PublicOnly: true}}}}); err == nil {
		t.Fatal("restriction became permission to bypass a lock")
	}
	for _, rule := range []api.Rule{{Allow: false, Target: "api.example.com", PublicOnly: true}, {Allow: true, Door: api.DoorPush, Target: "github.com/team/project", PublicOnly: true}, {Allow: true, Door: api.DoorPort, Target: "8080", PublicOnly: true}} {
		if _, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{rule}}); err == nil {
			t.Fatal("unsupported restriction was silently ignored")
		}
	}
}
