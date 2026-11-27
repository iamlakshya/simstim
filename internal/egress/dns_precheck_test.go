package egress

import (
	"context"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
)

// Use the standard refused range itself as test data, rather than recording
// a private host address in this public repository.
func dnsPrivateAddressFixture(t *testing.T) netip.Addr {
	t.Helper()
	prefix, err := ParseRange("10/8")
	if err != nil {
		t.Fatal(err)
	}
	return prefix.Addr().Next()
}

type dnsLedgerState struct {
	state slotState
	owner *Permit
}

func snapshotDNSLedger(eng *Engine) map[onceKey]dnsLedgerState {
	eng.ledger.mu.Lock()
	defer eng.ledger.mu.Unlock()
	out := make(map[onceKey]dnsLedgerState, len(eng.ledger.slots))
	for key, slot := range eng.ledger.slots {
		out[key] = dnsLedgerState{state: slot.state, owner: slot.owner}
	}
	return out
}

func dnsPrecheckServer(t *testing.T, pol api.Policy, dialer *Dialer) *Server {
	t.Helper()
	eng, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{engine: eng, inited: true, dnsMode: DNSModeReal, dialer: dialer, now: time.Now}
}

func answerDNSPrecheck(t *testing.T, srv *Server, host string, want dnsmessage.RCode) {
	t.Helper()
	query := buildSingleDNSQuery(t, 1, true, host+".", dnsmessage.TypeA, dnsmessage.ClassINET)
	msg := unpackDNSReply(t, srv.AnswerDNS(context.Background(), query))
	if msg.Header.RCode != want {
		t.Fatalf("DNS %s = %v, want %v", host, msg.Header.RCode, want)
	}
	if want != dnsmessage.RCodeSuccess && len(msg.Answers) != 0 {
		t.Fatalf("refused DNS %s returned %d answers", host, len(msg.Answers))
	}
	if want == dnsmessage.RCodeSuccess && len(msg.Answers) == 0 {
		t.Fatalf("granted DNS %s returned no address", host)
	}
}

func TestDNSPrecheckLeavesHostAndNumericOnceLedgerUntouched(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked,
		api.Rule{Allow: true, Target: "once.example.com:8443", Once: true},
		api.Rule{Allow: true, Target: "10/8:8443", Once: true},
	)
	resolver := newFakeResolver(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{dnsPrivateAddressFixture(t), netip.MustParseAddr("::ffff:" + dnsPrivateAddressFixture(t).String())}, nil
	})
	srv := dnsPrecheckServer(t, pol, &Dialer{Lookup: resolver.lookup})
	eng := srv.engine
	before := snapshotDNSLedger(eng)
	for range 4 {
		answerDNSPrecheck(t, srv, "once.example.com", dnsmessage.RCodeSuccess)
	}
	if after := snapshotDNSLedger(eng); !reflect.DeepEqual(after, before) {
		t.Fatalf("DNS changed available host or numeric once slots: before=%v after=%v", before, after)
	}

	permit := eng.NewPermit()
	t.Cleanup(func() { permit.Close() })
	req := Request{Host: "once.example.com", Port: 8443}
	if !eng.DecideWithPermit(req, permit).Allow || !eng.ReserveAddr(dnsPrivateAddressFixture(t), 8443, permit) {
		t.Fatal("DNS spent a once grant needed by the first real flow")
	}
	reserved := snapshotDNSLedger(eng)
	calls := resolver.calls.Load()
	answerDNSPrecheck(t, srv, "once.example.com", dnsmessage.RCodeNameError)
	if resolver.calls.Load() != calls || !reflect.DeepEqual(snapshotDNSLedger(eng), reserved) {
		t.Fatal("DNS resolved a reserved hostname or changed its live flow reservations")
	}
	permit.Close()
	answerDNSPrecheck(t, srv, "once.example.com", dnsmessage.RCodeSuccess)
	if !reflect.DeepEqual(snapshotDNSLedger(eng), before) {
		t.Fatal("DNS changed a released once slot")
	}

	spent := eng.NewPermit()
	if !eng.DecideWithPermit(req, spent).Allow || !eng.ReserveAddr(dnsPrivateAddressFixture(t), 8443, spent) {
		t.Fatal("a failed attempt did not release its once grants")
	}
	spent.Commit()
	spent.Close()
	consumed := snapshotDNSLedger(eng)
	calls = resolver.calls.Load()
	answerDNSPrecheck(t, srv, "once.example.com", dnsmessage.RCodeNameError)
	if resolver.calls.Load() != calls || !reflect.DeepEqual(snapshotDNSLedger(eng), consumed) {
		t.Fatal("DNS resolved a consumed hostname or changed a consumed once slot")
	}
}

func TestDNSNumericPrecheckDoesNotBorrowAnotherFlowsReservation(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked,
		api.Rule{Allow: true, Target: "allowed.example.com:8443"},
		api.Rule{Allow: true, Target: "10/8:8443", Once: true},
	)
	resolver := newFakeResolver(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{dnsPrivateAddressFixture(t)}, nil
	})
	srv := dnsPrecheckServer(t, pol, &Dialer{Lookup: resolver.lookup})
	eng := srv.engine
	permit := eng.NewPermit()
	t.Cleanup(func() { permit.Close() })
	if !eng.ReserveAddr(dnsPrivateAddressFixture(t), 8443, permit) {
		t.Fatal("cannot reserve numeric once grant")
	}
	before := snapshotDNSLedger(eng)
	answerDNSPrecheck(t, srv, "allowed.example.com", dnsmessage.RCodeRefused)
	if !reflect.DeepEqual(snapshotDNSLedger(eng), before) {
		t.Fatal("DNS changed another flow's numeric reservation")
	}
	permit.Close()
	answerDNSPrecheck(t, srv, "allowed.example.com", dnsmessage.RCodeSuccess)
}

func TestDNSPrecheckPreservesUnusedOnceGrantsUnderPermanentOverlap(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked,
		api.Rule{Allow: true, Target: "allowed.example.com:8443", Once: true},
		api.Rule{Allow: true, Target: "*.example.com:8443"},
		api.Rule{Allow: true, Target: "10/8:8443", Once: true},
		api.Rule{Allow: true, Target: dnsPrivateAddressFixture(t).String() + ":8443"},
	)
	resolver := newFakeResolver(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{dnsPrivateAddressFixture(t)}, nil
	})
	srv := dnsPrecheckServer(t, pol, &Dialer{Lookup: resolver.lookup})
	before := snapshotDNSLedger(srv.engine)
	for range 3 {
		answerDNSPrecheck(t, srv, "allowed.example.com", dnsmessage.RCodeSuccess)
	}
	if !reflect.DeepEqual(snapshotDNSLedger(srv.engine), before) {
		t.Fatal("DNS spent an overlapping once grant instead of preserving both rules")
	}
}

func TestDNSPrecheckRejectsDetachedOnceGrant(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "once.example.com", Once: true, GrantID: "11111111111111111111111111111111"})
	resolver := newFakeResolver(nil)
	srv := dnsPrecheckServer(t, pol, &Dialer{Lookup: resolver.lookup})
	old := srv.engine
	pol.Rules[0].GrantID = "22222222222222222222222222222222"
	replacement, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}
	old.ledger.syncPolicy(replacement)
	before := snapshotDNSLedger(replacement)
	answerDNSPrecheck(t, srv, "once.example.com", dnsmessage.RCodeNameError)
	if resolver.calls.Load() != 0 || !reflect.DeepEqual(snapshotDNSLedger(replacement), before) {
		t.Fatal("DNS used a detached grant or changed its replacement")
	}
	srv.engine = replacement
	answerDNSPrecheck(t, srv, "once.example.com", dnsmessage.RCodeSuccess)
	if !reflect.DeepEqual(snapshotDNSLedger(replacement), before) {
		t.Fatal("DNS changed the replacement once grant")
	}
}

func TestDNSDefaultResolverUsesOnlyGrantedRootedNames(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "allowed.example.com"})
	resolver := &recordingResolver{addrs: []netip.Addr{netip.MustParseAddr("203.0.113.10")}}
	srv := dnsPrecheckServer(t, pol, &Dialer{resolver: resolver})
	answerDNSPrecheck(t, srv, "blocked.example.com", dnsmessage.RCodeNameError)
	answerDNSPrecheck(t, srv, "Allowed.Example.Com", dnsmessage.RCodeSuccess)
	want := []resolverCall{{network: "ip", host: "allowed.example.com."}}
	if got := resolver.recorded(); !slices.Equal(got, want) {
		t.Fatalf("DNS resolver calls = %v, want %v", got, want)
	}

	custom := newFakeResolver(nil)
	srv.dialer = &Dialer{Lookup: custom.lookup, resolver: resolver}
	answerDNSPrecheck(t, srv, "Allowed.Example.Com", dnsmessage.RCodeSuccess)
	if got := custom.recordedHosts(); !slices.Equal(got, []string{"allowed.example.com"}) {
		t.Fatalf("custom Lookup names = %v, want undotted canonical host", got)
	}
	if got := resolver.recorded(); !slices.Equal(got, want) {
		t.Fatalf("custom Lookup also used default resolver: %v", got)
	}
}

func TestDNSSyntheticUnknownUsesTypedFallback(t *testing.T) {
	for _, preset := range []api.Preset{api.PresetLocked, api.PresetBalanced, api.PresetOpen} {
		for _, unknown := range []api.Unknown{api.UnknownAsk, api.UnknownAllow, api.UnknownClassify, api.UnknownDeny} {
			t.Run(string(preset)+"/"+string(unknown), func(t *testing.T) {
				pol := api.Policy{
					Preset: preset, Unknown: unknown,
					Rules: []api.Rule{
						{ID: "preset:balanced", Layer: api.LayerSession, Target: "denied.example.com", Allow: false},
						{ID: "preset:locked", Layer: api.LayerSession, Target: "allowed.example.com/v1/%69tems", Allow: true},
					},
					Locks: []api.Lock{{Layer: api.LayerAll, Deny: []string{"locked.example.com"}}},
				}
				resolver := newFakeResolver(nil)
				srv := dnsPrecheckServer(t, pol, &Dialer{Lookup: resolver.lookup})
				srv.dnsMode, srv.synth = DNSModeSynthetic, newSyntheticTable(MaxSyntheticNames)
				want := dnsmessage.RCodeSuccess
				if unknown == api.UnknownDeny {
					want = dnsmessage.RCodeNameError
				}
				answerDNSPrecheck(t, srv, "unknown.example.com", want)
				answerDNSPrecheck(t, srv, "denied.example.com", dnsmessage.RCodeNameError)
				answerDNSPrecheck(t, srv, "locked.example.com", dnsmessage.RCodeNameError)
				answerDNSPrecheck(t, srv, "allowed.example.com", dnsmessage.RCodeSuccess)
				if resolver.calls.Load() != 0 {
					t.Fatal("synthetic policy classification sent a name upstream")
				}
			})
		}
	}
}

func TestDNSSyntheticUnavailableOnceFallsBackWithoutChangingReservation(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "once.example.com", Once: true})
	pol.Unknown = api.UnknownAsk
	resolver := newFakeResolver(nil)
	srv := dnsPrecheckServer(t, pol, &Dialer{Lookup: resolver.lookup})
	srv.dnsMode, srv.synth = DNSModeSynthetic, newSyntheticTable(MaxSyntheticNames)
	permit := srv.engine.NewPermit()
	t.Cleanup(func() { permit.Close() })
	if !srv.engine.DecideWithPermit(Request{Host: "once.example.com", Port: 443}, permit).Allow {
		t.Fatal("cannot reserve host grant")
	}
	before := snapshotDNSLedger(srv.engine)
	answerDNSPrecheck(t, srv, "once.example.com", dnsmessage.RCodeSuccess)
	if !reflect.DeepEqual(snapshotDNSLedger(srv.engine), before) || resolver.calls.Load() != 0 {
		t.Fatal("synthetic fallback borrowed a reservation or resolved upstream")
	}
}
