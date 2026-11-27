package egress

import (
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTPServiceClassesRequireExplicitNonbuiltinWritePaths(t *testing.T) {
	for _, host := range []string{"registry.npmjs.org", "REGISTRY.NPMJS.ORG.", "bucket.s3.amazonaws.com", "storage.googleapis.com", "account.blob.core.windows.net"} {
		kind, spliceOnly, err := httpClass(host)
		if err != nil || spliceOnly || kind != "registry" && kind != "object" {
			t.Fatalf("class(%q) = %q, splice=%v, %v", host, kind, spliceOnly, err)
		}
	}
	for _, host := range []string{"registry.npmjs.org.example.com", "s3.amazonaws.com.example.com", "blob.core.windows.net.example.com"} {
		kind, _, err := httpClass(host)
		if err != nil || kind != "" {
			t.Fatalf("class crossed a host boundary: %q, %q, %v", host, kind, err)
		}
	}
	for _, test := range []struct {
		target string
		layer  api.LayerKind
		want   bool
	}{
		{"registry.npmjs.org", api.LayerSession, false},
		{"registry.npmjs.org/pkg/", api.LayerSession, true},
		{"registry.npmjs.org/pkg/", api.LayerBuiltin, false},
	} {
		decision := Decision{Allow: true, Target: test.target, Match: &api.LayerRef{Kind: test.layer}}
		if explicitPathAllowance(decision) != test.want {
			t.Fatalf("write override for %+v = %v", test, !test.want)
		}
	}
}

func TestHTTPServiceBarrierSharesDNSAndConnectWitnesses(t *testing.T) {
	post := []string{"POST"}
	for _, path := range []string{"", "/pkg/"} {
		engine, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
			{Layer: api.LayerSession, Target: "registry.npmjs.org" + path, Allow: true, Methods: &post},
		}})
		if err != nil {
			t.Fatal(err)
		}
		want := path != ""
		request := Request{Host: "registry.npmjs.org", Port: 443, Method: "POST", Path: "/pkg/"}
		if engine.evaluateHTTP(request, nil, evalPrecheck).Allow != want || engine.permitsIntercept(request.Host, request.Port) != want || engine.evaluateDNS(request.Host, engine.now()).permitsReal() != want {
			t.Fatalf("DNS, CONNECT and actual class barrier differ for target path %q", path)
		}
	}
}

func TestHTTPNumericExceptionUsesTheSameMethodAndPreservesOpaqueChecks(t *testing.T) {
	get, post := []string{"GET"}, []string{"POST"}
	engine, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerSession, Target: "api.example.com/safe", Allow: true, Methods: &post},
		{Layer: api.LayerSession, Target: "both.example.com/safe", Allow: true},
		{Layer: api.LayerSession, Target: "127.0.0.1", Allow: true, Methods: &get},
	}})
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddr("127.0.0.1")
	if engine.precheckResolvedAddr("api.example.com", address, 443) || engine.PrecheckAddr(address, 443, nil) {
		t.Fatal("disjoint name/address methods or an opaque request gained a numeric exception")
	}
	request := Request{Host: "both.example.com", Port: 443, Method: "GET", Path: "/safe"}
	if !engine.evaluateAddrRequest(address, 443, nil, evalPrecheck, &request) {
		t.Fatal("matching inspected numeric method was refused")
	}
	request.Method = "POST"
	if engine.evaluateAddrRequest(address, 443, nil, evalPrecheck, &request) {
		t.Fatal("numeric method exception broadened to another method")
	}
}
