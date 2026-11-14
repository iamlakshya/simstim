package egress

import (
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTPMethodRulesSharePrecedenceAndOpaqueDenial(t *testing.T) {
	get, post, empty := []string{"GET"}, []string{"POST"}, []string{}
	for _, methods := range []*[]string{&get, &post, &empty} {
		rule := api.Rule{Layer: api.LayerSession, Door: api.DoorNet, Target: "api.example.com", Allow: true, Methods: methods}
		engine, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{rule}})
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"GET", "POST", "get", "CONNECT"} {
			decision := engine.Decide(Request{Host: "api.example.com", Port: 443, Path: "/", Method: method})
			want := len(*methods) != 0 && (*methods)[0] == method
			if decision.Allow != want {
				t.Fatalf("methods=%v method=%q allow=%v want=%v", *methods, method, decision.Allow, want)
			}
		}
		if engine.Decide(Request{Host: "api.example.com", Port: 443, Method: "GET"}).Allow {
			t.Fatal("method rule authorised an opaque stream")
		}
		if engine.permitsIntercept("api.example.com", 443) != (len(*methods) != 0) {
			t.Fatal("finite method precheck disagreed with shared engine")
		}
	}
	engine, err := NewEngine("", api.Policy{Preset: api.PresetOpen, Rules: []api.Rule{
		{Layer: api.LayerSession, Target: "api.example.com", Methods: &get},
		{Layer: api.LayerSession, Target: "api.example.com", Methods: &post, Allow: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if engine.Decide(Request{Host: "api.example.com", Port: 443, Path: "/", Method: "GET"}).Allow || !engine.Decide(Request{Host: "api.example.com", Port: 443, Path: "/", Method: "POST"}).Allow {
		t.Fatal("method-scoped deny leaked to a disjoint method or was bypassed")
	}
	if !engine.hasInspectionRule("api.example.com", 443) {
		t.Fatal("method deny could be bypassed by an opaque tunnel")
	}
}

func TestHTTPMethodOncePrecheckDoesNotReserveAndRequestsDoNotShare(t *testing.T) {
	methods := []string{"POST"}
	engine, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerSession, Target: "api.example.com/write", Allow: true, Methods: &methods, Once: true, GrantID: "test-instance"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if !engine.permitsIntercept("api.example.com", 443) || !engine.evaluateDNS("api.example.com", engine.now()).permitsReal() {
			t.Fatal("read-only precheck consumed method-qualified once grant")
		}
	}
	request := Request{Host: "api.example.com", Port: 443, Method: "POST", Path: "/write"}
	first, second := engine.NewPermit(), engine.NewPermit()
	if !engine.DecideWithPermit(request, first).Allow || engine.DecideWithPermit(request, second).Allow {
		t.Fatal("concurrent requests shared a one-use reservation")
	}
	first.Close()
	if !engine.DecideWithPermit(request, second).Allow {
		t.Fatal("failed uncommitted request did not release its own reservation")
	}
	second.Commit()
	ended := second.Close()
	if len(ended) != 1 || ended[0].GrantID != "test-instance" || engine.DecideWithPermit(request, engine.NewPermit()).Allow {
		t.Fatal("committed request rearmed a one-use grant or lost exact end identity")
	}
}

func TestHTTPMethodQualifiedRuleCannotChooseAnArbitraryIdentity(t *testing.T) {
	methods := []string{"GET"}
	for _, layer := range []bool{false, true} {
		rule := api.Rule{ID: "chosen", Layer: api.LayerSession, Target: "api.example.com", Allow: true, Methods: &methods}
		var err error
		if layer {
			_, _, err = MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{rule}}})
		} else {
			_, err = NewEngine("", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{rule}})
		}
		if err == nil {
			t.Fatal("supplied constrained identity bypassed canonical identity")
		}
	}
}

func TestHTTPMethodScopedLocksReachActualHostAndAddressDecisions(t *testing.T) {
	get := []string{"GET"}
	engine, err := NewEngine("", api.Policy{Preset: api.PresetOpen,
		Order: []api.LayerRef{{Kind: api.LayerAll}, {Kind: api.LayerSession}},
		Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "127.0.0.1"}},
		Locks: []api.Lock{{Layer: api.LayerAll, LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{
			{Target: "api.example.com", Methods: &get}, {Target: "127.0.0.1", Methods: &get},
		}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.Decide(Request{Host: "api.example.com", Port: 443, Path: "/", Method: "GET"}).Allow || !engine.Decide(Request{Host: "api.example.com", Port: 443, Path: "/", Method: "POST"}).Allow {
		t.Fatal("scoped host lock was ignored or broadened to every method")
	}
	address := netip.MustParseAddr("127.0.0.1")
	for _, method := range []string{"GET", "POST"} {
		request := Request{Host: "other.example.com", Port: 443, Path: "/", Method: method}
		if engine.evaluateAddrRequest(address, 443, nil, evalPrecheck, &request) != (method == "POST") {
			t.Fatalf("address lock method %q was ignored or broadened", method)
		}
	}
	if !engine.hasInspectionRule("other.example.com", 443) {
		t.Fatal("numeric scoped lock was bypassed through a DNS hostname tunnel")
	}
}
