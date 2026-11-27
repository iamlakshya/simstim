package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestUnknownDestinationMetadataReusesCapacityWithoutAllocatingRoutes(t *testing.T) {
	table := newSyntheticTable(2)
	before := table.next
	if !table.reserveUnknown("real.example") || !table.reserveUnknown("1.1.1.1") || table.next != before || len(table.byAddr) != 0 {
		t.Fatal("observation-only real/direct-IP record allocated a synthetic route")
	}
	id := table.unknownID("real.example")
	if id == 0 || !table.reserveUnknown("real.example") || table.reserveUnknown("overflow.example") || len(table.byHost) != 2 || cap(table.byID) > table.limit {
		t.Fatal("metadata did not share the exact existing bounded destination records")
	}
	address, ok := table.assign("real.example")
	if !ok || table.unknownID("real.example") != id || len(table.byHost) != 2 || len(table.byAddr) != 1 {
		t.Fatal("later routing replaced an observation or spent another record")
	}
	if host, ok := table.lookupHost(address); !ok || host != "real.example" {
		t.Fatal("existing synthetic attribution changed")
	}
}

func TestUnknownDestinationAcceptedElectionIsConcurrentAndColdScoped(t *testing.T) {
	table := newSyntheticTable(2)
	if !table.reserveUnknown("authored.example") {
		t.Fatal("metadata fixture was not reserved")
	}
	id := table.unknownID("authored.example")
	// A failed or cancelled flow has no accepted-audit call and therefore
	// cannot mark this record. The first later accepted audit still wins.
	var firsts, unknown atomic.Int64
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			first, known := table.acceptUnknown(id)
			if first {
				firsts.Add(1)
			}
			if !known {
				unknown.Add(1)
			}
		})
	}
	workers.Wait()
	if firsts.Load() != 1 || unknown.Load() != 0 {
		t.Fatal("concurrent accepted audits did not elect exactly one existing record")
	}
	if first, known := table.acceptUnknown(0); first || known {
		t.Fatal("an unowned audit ID gained bookkeeping meaning")
	}
	cold := newSyntheticTable(2)
	cold.reserveUnknown("authored.example")
	if first, known := cold.acceptUnknown(cold.unknownID("authored.example")); !known || !first {
		t.Fatal("a previous actor's flag survived a genuine cold destination store")
	}
}

type unknownHTTPFixture struct {
	env                      *testEnv
	lookups, dials, requests atomic.Int64
}

func newUnknownHTTPFixture(t *testing.T, policy api.Policy, mode string) *unknownHTTPFixture {
	t.Helper()
	fixture := new(unknownHTTPFixture)
	dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		fixture.lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
	}, DialTCP: func(_ context.Context, target netip.AddrPort) (net.Conn, error) {
		if target != netip.MustParseAddrPort("198.51.100.8:443") && target != netip.MustParseAddrPort("1.1.1.1:443") {
			return nil, errors.New("unknown fixture reached an unchecked literal")
		}
		fixture.dials.Add(1)
		client, origin := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveHTTPConnection(t.Context(), origin, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fixture.requests.Add(1)
				w.WriteHeader(200)
			}), nil)
		}()
		t.Cleanup(func() {
			_ = client.Close()
			_ = origin.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("owned unknown HTTP origin did not join")
			}
		})
		return client, nil
	}}
	fixture.env = startTestServer(t, dialer)
	fixture.env.call(t, CtlInit, InitArgs{SessionID: "unknown2", Agent: "shell", DNS: mode, Policy: policy})
	return fixture
}

func (f *unknownHTTPFixture) request(t *testing.T, host string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest("GET", "http://"+host+":443/authored", nil).WithContext(t.Context())
	response := httptest.NewRecorder()
	f.env.srv.proxyHTTPRequest(response, request, StreamHeader{Via: "explicit", Host: host, Port: 443}, "http", nil)
	return response
}

func TestUnknownHTTPRealAndDirectIPRecordsHaveNoSyntheticRouteAndSurviveLiveUpdate(t *testing.T) {
	for _, host := range []string{"authored.example", "1.1.1.1"} {
		policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow}
		fixture := newUnknownHTTPFixture(t, policy, DNSModeReal)
		if response := fixture.request(t, host); response.Code != 200 {
			t.Fatal("actual unknown allowance did not reach its checked owned origin")
		}
		fixture.env.call(t, CtlCapabilities, struct{}{}) // ordered control barrier
		fixture.env.mu.Lock()
		audit := fixture.env.audits[len(fixture.env.audits)-1]
		fixture.env.mu.Unlock()
		table := fixture.env.srv.synth
		if audit.Decision != "allow-unknown" || audit.UnknownDestination == 0 || len(table.byAddr) != 0 || table.next != syntheticFirstUint32 || fixture.requests.Load() != 1 {
			t.Fatal("actual non-synthetic flow lost its accepted-audit ID or allocated routing")
		}
		if host == "1.1.1.1" && fixture.lookups.Load() != 0 {
			t.Fatal("direct-IP bookkeeping sent a DNS lookup")
		}
		if first, known := table.acceptUnknown(audit.UnknownDestination); !known || !first {
			t.Fatal("real source record was not eligible after its actual audit")
		}
		fixture.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetOpen, Unknown: api.UnknownAllow}})
		if fixture.env.srv.synth != table {
			t.Fatal("live policy replacement reset first-host history")
		}
		if response := fixture.request(t, host); response.Code != 200 {
			t.Fatal("live update changed the underlying ordinary allowance")
		}
		if first, known := table.acceptUnknown(audit.UnknownDestination); !known || first {
			t.Fatal("live policy replacement repeated the first-host election")
		}
	}
}

func TestUnknownDestinationPressurePrecedesLedgerAndOriginButKeepsNamedGrants(t *testing.T) {
	policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow, Rules: []api.Rule{
		{Layer: api.LayerSession, Target: "named.example", Allow: true},
		{Layer: api.LayerSession, Target: "other.example", Allow: true, Once: true, GrantID: "unrelated"},
	}}
	fixture := newUnknownHTTPFixture(t, policy, DNSModeReal)
	fixture.env.srv.synth = newSyntheticTable(1)
	fixture.env.srv.synth.reserveUnknown("filled.example")
	engine := fixture.env.srv.currentEngine()
	before := snapshotDNSLedger(engine)
	response := fixture.request(t, "overflow.example")
	if response.Code != 403 || !strings.Contains(response.Body.String(), "records are full") || fixture.lookups.Load() != 0 || fixture.dials.Load() != 0 || fixture.requests.Load() != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
		t.Fatal("untrackable new allowance spent a lease or reached DNS/origin")
	}
	if response := fixture.request(t, "named.example"); response.Code != 200 || fixture.requests.Load() != 1 {
		t.Fatal("bounded unknown metadata blocked an existing named grant")
	}
}
