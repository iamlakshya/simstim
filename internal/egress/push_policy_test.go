package egress

import (
	"reflect"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func pushTestRule() api.Rule {
	return api.Rule{Layer: api.LayerSession, Door: api.DoorPush, Allow: true, Target: "github.com/org/repo", Once: true, GrantID: "push-once"}
}

func pushTestRequest(discovery bool) Request {
	if discovery {
		return Request{Host: "github.com", Port: 443, Method: "GET", Path: "/org/repo.git/info/refs", query: "service=git-receive-pack", scheme: "https"}
	}
	return Request{Host: "github.com", Port: 443, Method: "POST", Path: "/org/repo.git/git-receive-pack", scheme: "https"}
}

func TestPushDiscoveryNeverReservesPushAndPOSTOwnsItsOriginalPermit(t *testing.T) {
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{pushTestRule()}})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotDNSLedger(engine)
	for range 3 {
		request := pushTestRequest(true)
		route, _ := gitPushHTTPRoute(request)
		permit := engine.NewPermit()
		if !engine.admitPushRequest(request, route, permit, evalReserve, time.Time{}).Allow || len(permit.owned) != 0 {
			t.Fatal("discovery consumed the sole push lease")
		}
		permit.Commit()
		permit.Close()
	}
	if !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
		t.Fatal("discovery changed push availability")
	}
	request := pushTestRequest(false)
	route, _ := gitPushHTTPRoute(request)
	permit := engine.NewPermit()
	defer permit.Close()
	if !engine.admitPushRequest(request, route, permit, evalReserve, time.Time{}).Allow || len(permit.owned) != 1 {
		t.Fatal("POST did not acquire exactly its push lease")
	}
	permit.Commit()
	if !engine.evaluateHTTP(request, permit, evalRevalidate).Allow {
		t.Fatal("POST lost its own committed lease")
	}
	other := engine.NewPermit()
	defer other.Close()
	if engine.admitPushRequest(request, route, other, evalReserve, time.Time{}).Allow {
		t.Fatal("a second POST reused the owned push lease")
	}
	if engine.evaluateHTTP(pushTestRequest(true), nil, evalPrecheck).Allow {
		t.Fatal("discovery ignored current push availability")
	}
}

func TestPushGrantNeverBecomesBroadNetworkingAndSharesDeniesAndExpiry(t *testing.T) {
	grant := pushTestRule()
	for _, change := range []func(*Request){
		func(r *Request) { r.Path = "/org/other.git/git-receive-pack" },
		func(r *Request) {
			r.Path = "/org/repo.git/info/refs"
			r.Method = "GET"
			r.query = "service=git-receive-pack&x=1"
		},
		func(r *Request) { r.Method = "PUT" },
		func(r *Request) { r.query = "x=1" },
		func(r *Request) { r.forceQuery = true },
		func(r *Request) { r.scheme = "http" },
		func(r *Request) { r.Port = 80 },
		func(r *Request) { r.Path = "/org/repo.git/git-receive-pack/" },
	} {
		engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}})
		if err != nil {
			t.Fatal(err)
		}
		request := pushTestRequest(false)
		change(&request)
		if engine.evaluateHTTP(request, nil, evalPrecheck).Allow || engine.Decide(Request{Host: "github.com", Port: 443}).Allow {
			t.Fatal("push authority escaped its exact HTTPS protocol request")
		}
	}
	for _, lock := range []bool{false, true} {
		policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}
		if lock {
			policy.Locks = []api.Lock{{Layer: api.LayerHost, Deny: []string{"github.com"}}}
		} else {
			policy.Rules = append(policy.Rules, api.Rule{Layer: api.LayerSession, Target: "github.com"})
		}
		engine, err := NewEngine("shell", policy)
		if err != nil {
			t.Fatal(err)
		}
		permit := engine.NewPermit()
		request := pushTestRequest(false)
		route, _ := gitPushHTTPRoute(request)
		if engine.admitPushRequest(request, route, permit, evalReserve, time.Time{}).Allow || len(permit.owned) != 0 {
			t.Fatal("push acquired a lease across an ordinary deny or lock")
		}
		permit.Close()
	}
	now := time.Now()
	grant.Until = now
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}})
	if err != nil {
		t.Fatal(err)
	}
	engine.now = func() time.Time { return now }
	if engine.evaluateHTTP(pushTestRequest(true), nil, evalPrecheck).Allow {
		t.Fatal("expired push grant authorised discovery")
	}
}

func TestPushRevocationBetweenDiscoveryAndPOSTAndDuringItsOwnedFlow(t *testing.T) {
	policy := api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{pushTestRule()}}
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	if !engine.evaluateHTTP(pushTestRequest(true), nil, evalPrecheck).Allow {
		t.Fatal("initial discovery unavailable")
	}
	request := pushTestRequest(false)
	route, _ := gitPushHTTPRoute(request)
	permit := engine.NewPermit()
	defer permit.Close()
	if !engine.admitPushRequest(request, route, permit, evalReserve, time.Time{}).Allow {
		t.Fatal("initial POST unavailable")
	}
	permit.Commit()
	revoked, err := NewEngine("shell", api.Policy{Preset: api.PresetBalanced})
	if err != nil {
		t.Fatal(err)
	}
	engine.ledger.syncPolicy(revoked)
	service := &serviceRequest{request: request, pushRepo: route.repo}
	if service.allowed(revoked, permit) {
		t.Fatal("revoked push flow fell back to balanced host networking")
	}
	other := revoked.NewPermit()
	defer other.Close()
	if revoked.admitPushRequest(request, route, other, evalReserve, time.Time{}).Allow {
		t.Fatal("discovery cached authority after revocation")
	}
}
