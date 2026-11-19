package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestUnknownAllowPublicLiteralKeepsIndependentAddressExceptions(t *testing.T) {
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		address netip.Addr
		public  bool
	}{
		{netip.AddrFrom4([4]byte{1, 1, 1, 1}), true},
		{netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 1, 13: 1, 14: 1, 15: 1}), true},
		{netip.MustParseAddr("2606:4700:4700::1111"), true},
		{netip.AddrFrom4([4]byte{10, 0, 0, 1}), false},
		{netip.AddrFrom4([4]byte{169, 254, 169, 254}), false},
		{netip.AddrFrom4([4]byte{100, 64, 0, 1}), false},
		{netip.AddrFrom4([4]byte{127, 0, 0, 1}), false},
		{netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 10, 15: 1}), false},
		{netip.AddrFrom16([16]byte{0: 0xfc, 15: 1}), false},
		{netip.AddrFrom16([16]byte{0: 0xfe, 1: 0x80, 15: 1}), false},
		{netip.AddrFrom16([16]byte{}), false},
		{netip.AddrFrom16([16]byte{1: 0x64, 2: 0xff, 3: 0x9b, 12: 10, 15: 1}), false},
	} {
		t.Run(tc.address.String(), func(t *testing.T) {
			address := tc.address
			permit := engine.NewPermit()
			defer permit.Close()
			before := snapshotDNSLedger(engine)
			got := engine.DecideWithPermit(Request{Host: address.String(), Port: 443}, permit)
			if got.Allow != tc.public || !got.unmatched || len(permit.owned) != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
				t.Fatal("ordinary public fallback bypassed address safety or changed a lease")
			}
			if engine.AllowsAddr(address, 443) || engine.PrecheckAddr(address, 443, permit) {
				t.Fatal("unknown action became an explicit numeric exception")
			}
		})
	}
	open, err := NewEngine("shell", api.Policy{Preset: api.PresetOpen})
	if err != nil || open.Decide(Request{Host: "1.1.1.1", Port: 443}).Allow {
		t.Fatal("the original preset-open numeric fallback changed without UnknownAllow", err)
	}
}

func TestUnknownAllowPublicLiteralKeepsLocksDeniesAndPrivateNamedGrants(t *testing.T) {
	for _, lock := range []bool{false, true} {
		policy := api.Policy{Preset: api.PresetOpen, Unknown: api.UnknownAllow}
		if lock {
			policy.Locks = []api.Lock{{Layer: api.LayerAll, Deny: []string{"1.1.1.1"}}}
		} else {
			policy.Rules = []api.Rule{{Layer: api.LayerSession, Target: "1.1.1.1"}}
		}
		engine, err := NewEngine("shell", policy)
		if err != nil || engine.Decide(Request{Host: "1.1.1.1", Port: 443}).Allow {
			t.Fatal("public fallback lifted an actual lock or deny", err)
		}
	}
	privateOne := netip.AddrFrom4([4]byte{10, 0, 0, 1})
	privateOther := netip.AddrFrom4([4]byte{10, 0, 0, 2})
	named, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow, Rules: []api.Rule{{Layer: api.LayerSession, Target: privateOne.String(), Allow: true}}})
	if err != nil || !named.AllowsAddr(privateOne, 443) {
		t.Fatal("the original explicit numeric exception contract was removed", err)
	}
	if named.AllowsAddr(privateOther, 443) {
		t.Fatal("a named exception and UnknownAllow became a private-range blanket grant")
	}
}

func TestUnknownAllowLiteralActualCheckedDialAndOwnHostRevalidation(t *testing.T) {
	for _, host := range []string{"1.1.1.1", "::ffff:1.1.1.1", "2606:4700:4700::1111"} {
		for _, ownHost := range []bool{false, true} {
			engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow})
			if err != nil {
				t.Fatal(err)
			}
			request := Request{Host: host, Port: 443, Method: "TCP"}
			permit := engine.NewPermit()
			defer permit.Close()
			if !engine.DecideWithPermit(request, permit).Allow {
				t.Fatal("public literal fixture was not admitted")
			}
			var lookups, dials atomic.Int64
			dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
				lookups.Add(1)
				return nil, errors.New("literal fixture forbids DNS")
			}, DialTCP: func(_ context.Context, target netip.AddrPort) (net.Conn, error) {
				dials.Add(1)
				if target.Addr().Unmap() != netip.MustParseAddr(host).Unmap() || target.Port() != 443 {
					return nil, errors.New("checked literal identity changed")
				}
				client, peer := net.Pipe()
				t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
				return client, nil
			}}
			if ownHost {
				dialer.HostAddrs = []netip.Addr{netip.MustParseAddr(host).Unmap()}
			}
			conn, _, _, err := dialer.dialWithPermit(t.Context(), func() *Engine { return engine }, host, 443, permit, &request)
			if lookups.Load() != 0 {
				t.Fatal("literal fallback performed a DNS lookup")
			}
			if ownHost {
				if conn != nil || !errors.Is(err, ErrRefusedAddr) || dials.Load() != 0 {
					t.Fatal("public policy fallback bypassed current own-host address protection")
				}
			} else if err != nil || conn == nil || dials.Load() != 1 {
				t.Fatal("public literal did not use the original checked dial path", err)
			}
		}
	}
}

func TestUnknownAllowLiteralPolicyReplacementStopsProspectiveAndLiveFlow(t *testing.T) {
	old, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: "1.1.1.1", Port: 443, Method: "TCP"}
	permit := old.NewPermit()
	defer permit.Close()
	if !old.DecideWithPermit(request, permit).Allow {
		t.Fatal("prospective fixture was not initially admitted")
	}
	live := old.NewPermit()
	defer live.Close()
	if !old.DecideWithPermit(request, live).Allow {
		t.Fatal("live fixture was not initially admitted")
	}
	live.Commit()
	replaced, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownDeny})
	if err != nil {
		t.Fatal(err)
	}
	old.ledger.syncPolicy(replaced)
	var dials atomic.Int64
	dialer := &Dialer{DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("revoked fixture reached physical dial")
	}}
	conn, _, _, err := dialer.dialWithPermit(t.Context(), func() *Engine { return replaced }, request.Host, request.Port, permit, &request)
	if conn != nil || !errors.Is(err, ErrRefusedAddr) || dials.Load() != 0 {
		t.Fatal("prospective public fallback escaped current policy revalidation")
	}
	flows := newFlowTracker(1)
	var cancelled atomic.Bool
	flows.add(activeFlow{request: request, addr: netip.MustParseAddr(request.Host), permit: live, cancel: func() { cancelled.Store(true) }})
	flows.revalidate(replaced, dialer)
	if !cancelled.Load() {
		t.Fatal("a live public fallback survived actual policy replacement")
	}
}

func TestUnknownAllowActualHTTPLiteralUsesCheckedOriginAndAuditDecision(t *testing.T) {
	var lookups, dials, requests atomic.Int64
	dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return nil, errors.New("literal HTTP fixture forbids DNS")
	}, DialTCP: func(_ context.Context, target netip.AddrPort) (net.Conn, error) {
		if target != netip.MustParseAddrPort("1.1.1.1:443") {
			return nil, errors.New("literal HTTP target changed")
		}
		dials.Add(1)
		client, origin := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = origin.Close() })
		go serveHTTPConnection(t.Context(), origin, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(200)
		}), nil)
		return client, nil
	}}
	env := startTestServer(t, dialer)
	env.call(t, CtlInit, InitArgs{SessionID: "literal2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow}})
	request := httptest.NewRequest("GET", "http://1.1.1.1:443/authored", nil).WithContext(t.Context())
	response := httptest.NewRecorder()
	audit := env.srv.proxyHTTPRequest(response, request, StreamHeader{Via: "explicit", Host: "1.1.1.1", Port: 443}, "http", nil).get()
	if response.Code != 200 || lookups.Load() != 0 || dials.Load() != 1 || requests.Load() != 1 || audit.Rule != "unknown:allow" || audit.Decision != "allow-unknown" {
		t.Fatal("actual literal HTTP fallback lost checked origin ownership or its allowance audit")
	}
}
