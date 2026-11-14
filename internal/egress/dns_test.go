package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type fakeResolver struct {
	calls atomic.Int64
	mu    sync.Mutex
	hosts []string
	fn    func(ctx context.Context, host string) ([]netip.Addr, error)
}

func newFakeResolver(fn func(ctx context.Context, host string) ([]netip.Addr, error)) *fakeResolver {
	return &fakeResolver{fn: fn}
}

func (r *fakeResolver) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	r.calls.Add(1)
	r.mu.Lock()
	r.hosts = append(r.hosts, host)
	r.mu.Unlock()
	if r.fn == nil {
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
	}
	return r.fn(ctx, host)
}

func (r *fakeResolver) recordedHosts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.hosts)
}

func queryDNSStream(t *testing.T, sess *link.Session, raw []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.KindDNS, nil)
	if err != nil {
		t.Fatalf("Open(KindDNS): %v", err)
	}
	defer st.Close()
	if _, err := st.Write(raw); err != nil {
		t.Fatalf("Write(KindDNS): %v", err)
	}
	if err := st.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite(KindDNS): %v", err)
	}
	resp, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("ReadAll(KindDNS): %v", err)
	}
	return resp
}

func unpackDNSReply(t *testing.T, raw []byte) dnsmessage.Message {
	t.Helper()
	var msg dnsmessage.Message
	if err := msg.Unpack(raw); err != nil {
		t.Fatalf("Unpack DNS reply (%x): %v", raw, err)
	}
	return msg
}

func extractReplyAddrs(t *testing.T, msg dnsmessage.Message) []netip.Addr {
	t.Helper()
	var out []netip.Addr
	for _, ans := range msg.Answers {
		switch body := ans.Body.(type) {
		case *dnsmessage.AResource:
			out = append(out, netip.AddrFrom4(body.A))
		case *dnsmessage.AAAAResource:
			out = append(out, netip.AddrFrom16(body.AAAA))
		default:
			t.Fatalf("unexpected answer RR body %T", ans.Body)
		}
	}
	return out
}

func callCtlRaw(t *testing.T, e *testEnv, op string, args any) proto.Response {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan proto.Response, 1)
	e.mu.Lock()
	e.nextID++
	id := e.nextID
	e.waits[id] = ch
	e.mu.Unlock()
	if err := e.ctlW.WriteJSON(proto.TRequest, proto.Request{ID: id, Op: op, Args: b}); err != nil {
		t.Fatal(err)
	}
	select {
	case resp := <-ch:
		return resp
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s response", op)
		return proto.Response{}
	}
}

func TestDNSDeniedBalancedZeroUpstreamCalls(t *testing.T) {
	fr := newFakeResolver(func(_ context.Context, host string) ([]netip.Addr, error) {
		t.Errorf("unexpected upstream lookup for ungranted host %q", host)
		return []netip.Addr{netip.MustParseAddr("203.0.113.99")}, nil
	})
	env := startTestServer(t, &Dialer{Lookup: fr.lookup})
	pol, _, err := MergeLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Even with UnknownAllow set, real-mode DNS never resolves ungranted names.
	pol.Unknown = api.UnknownAllow
	env.call(t, CtlInit, InitArgs{
		SessionID:  "abcd2345",
		SessionRef: "work-box",
		Agent:      "claude",
		DNS:        DNSModeReal,
		Policy:     pol,
	})

	for _, qt := range []dnsmessage.Type{
		dnsmessage.TypeA,
		dnsmessage.TypeAAAA,
		dnsmessage.TypeHTTPS,
		dnsmessage.TypeSVCB,
		dnsmessage.TypeTXT,
	} {
		raw := buildSingleDNSQuery(t, 0x1122, true, "example.com.", qt, dnsmessage.ClassINET)
		resp := queryDNSStream(t, env.supSess, raw)
		msg := unpackDNSReply(t, resp)
		if msg.Header.RCode != dnsmessage.RCodeNameError || len(msg.Answers) != 0 {
			t.Fatalf("denied example.com (%v) = rcode %v answers %d, want NXDOMAIN with 0 answers", qt, msg.Header.RCode, len(msg.Answers))
		}
	}
	if got := fr.calls.Load(); got != 0 {
		t.Fatalf("fake resolver calls = %d, want 0", got)
	}
}

func TestDNSGrantedFamiliesNormalisationExpiryAndOnce(t *testing.T) {
	nowAtomic := atomic.Int64{}
	t0 := time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)
	nowAtomic.Store(t0.UnixNano())
	clock := func() time.Time { return time.Unix(0, nowAtomic.Load()).UTC() }

	fr := newFakeResolver(func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "api.example.com":
			return []netip.Addr{
				netip.MustParseAddr("203.0.113.10"),
				netip.MustParseAddr("::ffff:203.0.113.11"),
				netip.MustParseAddr("2001:db8::9"),
			}, nil
		case "v4only.example.com":
			return []netip.Addr{netip.MustParseAddr("203.0.113.44")}, nil
		case "once.example.com":
			return []netip.Addr{netip.MustParseAddr("203.0.113.55")}, nil
		case "expired.example.com":
			t.Errorf("expired grant triggered upstream lookup for %q", host)
			return []netip.Addr{netip.MustParseAddr("203.0.113.66")}, nil
		default:
			t.Errorf("unexpected host %q", host)
			return nil, errors.New("unexpected host")
		}
	})

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	c1, c2 := net.Pipe()
	srv, err := NewServer(c2, Options{
		Now: clock,
		Dialer: &Dialer{
			Lookup: fr.lookup,
			DialTCP: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
				var nd net.Dialer
				return nd.DialContext(ctx, "tcp", echoLn.Addr().String())
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	supSess := link.New(c1, link.Supervisor, LinkOptions())
	t.Cleanup(func() {
		_ = supSess.Close()
		_ = srv.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctlSt, err := supSess.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{
		supSess: supSess,
		ctlW:    proto.NewWriter(ctlSt),
		ctlR:    bufio.NewReader(ctlSt),
		srv:     srv,
		waits:   map[uint64]chan proto.Response{},
	}
	go env.readCtl()

	pol, _, err := MergeLayers([]api.Layer{{
		Kind:   api.LayerSession,
		Preset: api.PresetLocked,
		Rules: []api.Rule{
			{Allow: true, Door: api.DoorNet, Target: "api.example.com"},
			{Allow: true, Door: api.DoorNet, Target: "v4only.example.com"},
			{Allow: true, Door: api.DoorNet, Target: "once.example.com:80", Once: true},
			{Allow: true, Door: api.DoorNet, Target: "expired.example.com", Until: t0.Add(time.Minute)},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	env.call(t, CtlInit, InitArgs{
		SessionID:  "abcd2345",
		SessionRef: "work-box",
		DNS:        DNSModeReal,
		Policy:     pol,
	})

	// 1. Mixed-case 0x20 A query normalises lookup host, echoes exact QName/ID/RD,
	// unmaps ::ffff:203.0.113.11 into A, and reaches fake resolver once.
	callsBefore := fr.calls.Load()
	qA := buildSingleDNSQuery(t, 0x4242, true, "ApI.eXaMpLe.CoM.", dnsmessage.TypeA, dnsmessage.ClassINET)
	rawA := queryDNSStream(t, supSess, qA)
	msgA := unpackDNSReply(t, rawA)
	if fr.calls.Load()-callsBefore != 1 {
		t.Fatalf("A query resolver calls = %d, want 1", fr.calls.Load()-callsBefore)
	}
	if hosts := fr.recordedHosts(); len(hosts) == 0 || hosts[len(hosts)-1] != "api.example.com" {
		t.Fatalf("recorded lookup hosts = %v, want canonical api.example.com", hosts)
	}
	if msgA.Header.ID != 0x4242 || !msgA.Header.Response || !msgA.Header.RecursionDesired || !msgA.Header.RecursionAvailable || msgA.Header.Authoritative || rawA[3]&0x20 != 0 || msgA.Header.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("A reply header = %+v (flags2=%#x)", msgA.Header, rawA[3])
	}
	if len(msgA.Questions) != 1 || msgA.Questions[0].Name.String() != "ApI.eXaMpLe.CoM." {
		t.Fatalf("echoed question = %+v", msgA.Questions)
	}
	gotV4 := extractReplyAddrs(t, msgA)
	wantV4 := []netip.Addr{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("203.0.113.11")}
	if !slices.Equal(gotV4, wantV4) {
		t.Fatalf("A answers = %v, want %v", gotV4, wantV4)
	}
	for _, ans := range msgA.Answers {
		if ans.Header.Name.String() != "ApI.eXaMpLe.CoM." || ans.Header.TTL != dnsAnswerTTL {
			t.Fatalf("A answer header = %+v", ans.Header)
		}
	}

	// 2. AAAA query emits pure IPv6 only, never IPv4-mapped IPv6.
	callsBefore = fr.calls.Load()
	qAAAA := buildSingleDNSQuery(t, 0x4243, false, "ApI.eXaMpLe.CoM.", dnsmessage.TypeAAAA, dnsmessage.ClassINET)
	msgAAAA := unpackDNSReply(t, queryDNSStream(t, supSess, qAAAA))
	if fr.calls.Load()-callsBefore != 1 {
		t.Fatalf("AAAA query resolver calls = %d, want 1", fr.calls.Load()-callsBefore)
	}
	gotV6 := extractReplyAddrs(t, msgAAAA)
	wantV6 := []netip.Addr{netip.MustParseAddr("2001:db8::9")}
	if !slices.Equal(gotV6, wantV6) {
		t.Fatalf("AAAA answers = %v, want %v", gotV6, wantV6)
	}

	// 3. Allowed host with no AAAA records returns NOERROR with 0 answers (NODATA).
	qNodata := buildSingleDNSQuery(t, 0x4244, true, "v4only.example.com.", dnsmessage.TypeAAAA, dnsmessage.ClassINET)
	msgNodata := unpackDNSReply(t, queryDNSStream(t, supSess, qNodata))
	if msgNodata.Header.RCode != dnsmessage.RCodeSuccess || len(msgNodata.Answers) != 0 {
		t.Fatalf("NODATA reply = rcode %v answers %d, want NOERROR 0 answers", msgNodata.Header.RCode, len(msgNodata.Answers))
	}

	// 4. Advance clock to Until expiry: expired grant returns NXDOMAIN with 0 calls.
	nowAtomic.Store(t0.Add(time.Minute).UnixNano())
	callsBefore = fr.calls.Load()
	qExp := buildSingleDNSQuery(t, 0x4245, true, "expired.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
	msgExp := unpackDNSReply(t, queryDNSStream(t, supSess, qExp))
	if msgExp.Header.RCode != dnsmessage.RCodeNameError || fr.calls.Load() != callsBefore {
		t.Fatalf("expired grant = rcode %v calls +%d, want NXDOMAIN +0", msgExp.Header.RCode, fr.calls.Load()-callsBefore)
	}

	// 5. Multiple DNS queries do NOT consume a --once grant; one real flow remains
	// allowed, the second flow is denied, and a later DNS query gets NXDOMAIN + 0 calls.
	for i := range 4 {
		qOnce := buildSingleDNSQuery(t, uint16(0x5000+i), true, "once.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
		msgOnce := unpackDNSReply(t, queryDNSStream(t, supSess, qOnce))
		if msgOnce.Header.RCode != dnsmessage.RCodeSuccess || len(msgOnce.Answers) != 1 {
			t.Fatalf("once DNS query %d = rcode %v answers %d", i, msgOnce.Header.RCode, len(msgOnce.Answers))
		}
	}

	stFlow1, err := supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "once.example.com", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stFlow1, "CONNECT once.example.com:80 HTTP/1.1\r\nHost: once.example.com:80\r\n\r\n")
	respFlow1, err := http.ReadResponse(bufio.NewReader(stFlow1), nil)
	if err != nil || respFlow1.StatusCode != http.StatusOK {
		t.Fatalf("first flow after DNS on once grant = %v, %v; want 200 OK", respFlow1, err)
	}
	_ = respFlow1.Body.Close()
	_ = stFlow1.Close()

	stFlow2, err := supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "once.example.com", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stFlow2, "CONNECT once.example.com:80 HTTP/1.1\r\nHost: once.example.com:80\r\n\r\n")
	respFlow2, err := http.ReadResponse(bufio.NewReader(stFlow2), nil)
	if err != nil || respFlow2.StatusCode != http.StatusForbidden {
		t.Fatalf("second flow on once grant = %v, %v; want 403 Forbidden", respFlow2, err)
	}
	_ = respFlow2.Body.Close()
	_ = stFlow2.Close()

	callsBefore = fr.calls.Load()
	qConsumed := buildSingleDNSQuery(t, 0x5099, true, "once.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
	msgConsumed := unpackDNSReply(t, queryDNSStream(t, supSess, qConsumed))
	if msgConsumed.Header.RCode != dnsmessage.RCodeNameError || fr.calls.Load() != callsBefore {
		t.Fatalf("consumed once grant DNS = rcode %v calls +%d, want NXDOMAIN +0", msgConsumed.Header.RCode, fr.calls.Load()-callsBefore)
	}
}

func TestDNSPolicyProjectionLayersLocksPortsAndPaths(t *testing.T) {
	fr := newFakeResolver(func(_ context.Context, _ string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.20")}, nil
	})
	env := startTestServer(t, &Dialer{Lookup: fr.lookup})

	pol, _, err := MergeLayers([]api.Layer{
		{
			Kind:   api.LayerAll,
			Preset: api.PresetBalanced,
			Rules: []api.Rule{
				{Allow: true, Door: api.DoorNet, Target: "*.example.com"},
				{Allow: false, Door: api.DoorNet, Target: "*.corp2.example"},
				{Allow: true, Door: api.DoorNet, Target: "site.example.org"},
				{Allow: true, Door: api.DoorNet, Target: "overridden.example.org/v1/ok"},
				{Allow: true, Door: api.DoorNet, Target: "partial.example.org/v1/"},
			},
			Lock: &api.Lock{
				Deny: []string{"locked.example.net", "*.secret.example", "site.example.org/admin/"},
			},
		},
		{
			Kind: api.LayerSession,
			Rules: []api.Rule{
				{Allow: false, Door: api.DoorNet, Target: "denied.example.com"},
				{Allow: false, Door: api.DoorNet, Target: "same.example.net"},
				{Allow: true, Door: api.DoorNet, Target: "same.example.net"},
				{Allow: true, Door: api.DoorNet, Target: "open.corp2.example"},
				{Allow: true, Door: api.DoorNet, Target: "customport.example.net:8443"},
				{Allow: true, Door: api.DoorNet, Target: "pathonly.example.net/v2/items"},
				{Allow: false, Door: api.DoorNet, Target: "api.github.com/user/repos"},
				{Allow: false, Door: api.DoorNet, Target: "overridden.example.org/v1/"},
				{Allow: false, Door: api.DoorNet, Target: "partial.example.org/v1/dns-probe"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", DNS: DNSModeReal, Policy: pol})

	wantAllow := []string{
		"sub.example.com.",
		"open.corp2.example.",
		"customport.example.net.",
		"pathonly.example.net.",
		"api.github.com.",
		"site.example.org.",
		"partial.example.org.",
	}
	for _, qn := range wantAllow {
		msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 1, true, qn, dnsmessage.TypeA, dnsmessage.ClassINET)))
		if msg.Header.RCode != dnsmessage.RCodeSuccess || len(msg.Answers) != 1 {
			t.Errorf("AnswerDNS(%s) = rcode %v answers %d, want NOERROR with 1 answer", qn, msg.Header.RCode, len(msg.Answers))
		}
	}

	callsBeforeDeny := fr.calls.Load()
	wantDeny := []string{
		"locked.example.net.",
		"api.secret.example.",
		"denied.example.com.",
		"same.example.net.",
		"blocked.corp2.example.",
		"overridden.example.org.",
	}
	for _, qn := range wantDeny {
		msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 2, true, qn, dnsmessage.TypeA, dnsmessage.ClassINET)))
		if msg.Header.RCode != dnsmessage.RCodeNameError || len(msg.Answers) != 0 {
			t.Errorf("AnswerDNS(%s) = rcode %v answers %d, want NXDOMAIN", qn, msg.Header.RCode, len(msg.Answers))
		}
	}
	if fr.calls.Load() != callsBeforeDeny {
		t.Fatalf("denied projections triggered %d upstream calls, want 0", fr.calls.Load()-callsBeforeDeny)
	}

	// Open preset with port-specific denies on 443 and 80 still resolves via unnamed port sentinel.
	polOpen, _, err := MergeLayers([]api.Layer{{
		Kind:   api.LayerSession,
		Preset: api.PresetOpen,
		Rules: []api.Rule{
			{Allow: false, Door: api.DoorNet, Target: "example.com:443"},
			{Allow: false, Door: api.DoorNet, Target: "example.com:80"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: polOpen})
	msgOpen := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 3, true, "example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)))
	if msgOpen.Header.RCode != dnsmessage.RCodeSuccess || len(msgOpen.Answers) != 1 {
		t.Fatalf("open preset with :443 and :80 denies = rcode %v answers %d, want NOERROR 1 answer", msgOpen.Header.RCode, len(msgOpen.Answers))
	}

	// Unknown validation in MergeLayers (values and locks) and NewEngine.
	for _, bad := range [][]api.Layer{
		{{Kind: api.LayerSession, Unknown: api.Unknown("bogus")}},
		{{Kind: api.LayerHost, Lock: &api.Lock{Unknown: api.Unknown("bogus")}}},
	} {
		_, _, mErr := MergeLayers(bad)
		var pe *proto.Error
		if !errors.As(mErr, &pe) || pe.Code != "bad_request" || pe.Hint == "" {
			t.Errorf("MergeLayers(bad unknown) = %v, want bad_request proto.Error with Hint", mErr)
		}
	}
	if _, nErr := NewEngine("shell", api.Policy{Preset: api.PresetBalanced, Unknown: api.Unknown("invalid")}); nErr == nil {
		t.Error("NewEngine accepted invalid Unknown")
	}
	_, _, lockErr := MergeLayers([]api.Layer{
		{Kind: api.LayerHost, Lock: &api.Lock{Unknown: api.UnknownDeny}},
		{Kind: api.LayerSession, Unknown: api.UnknownAsk},
	})
	var lockPE *proto.Error
	if !errors.As(lockErr, &lockPE) || lockPE.Code != "policy_locked" || lockPE.Hint == "" {
		t.Errorf("locked unknown override = %v, want policy_locked with Hint", lockErr)
	}
}

func TestDNSSyntheticModesStabilityPolicyUpdatesAndExhaustion(t *testing.T) {
	fr := newFakeResolver(func(_ context.Context, host string) ([]netip.Addr, error) {
		t.Errorf("synthetic DNS unexpectedly queried upstream for %q", host)
		return nil, errors.New("unexpected upstream call")
	})
	var env *testEnv
	synthPrefix := netip.MustParsePrefix("198.18.0.0/15")

	for _, unk := range []api.Unknown{api.UnknownAsk, api.UnknownAllow, api.UnknownClassify} {
		env = startTestServer(t, &Dialer{Lookup: fr.lookup})
		pol, _, err := MergeLayers([]api.Layer{
			{
				Kind:    api.LayerAll,
				Preset:  api.PresetBalanced,
				Unknown: unk,
				Lock:    &api.Lock{Deny: []string{"locked-deny.example.com"}},
			},
			{
				Kind: api.LayerSession,
				Rules: []api.Rule{
					{Allow: false, Door: api.DoorNet, Target: "explicit-deny.example.com"},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", DNS: DNSModeSynthetic, Policy: pol})

		// First synthetic address is 198.18.0.2 (reserving .0.1, excluding .0.0).
		msg1 := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 10, true, "Alpha.Unknown.Example.", dnsmessage.TypeA, dnsmessage.ClassINET)))
		addrs1 := extractReplyAddrs(t, msg1)
		if msg1.Header.RCode != dnsmessage.RCodeSuccess || len(addrs1) != 1 || addrs1[0] != netip.MustParseAddr("198.18.0.2") || !synthPrefix.Contains(addrs1[0]) {
			t.Fatalf("unknown=%s first synthetic A = %v (%v), want [198.18.0.2]", unk, addrs1, msg1.Header.RCode)
		}
		// Repeat query (different case) returns the identical address; reverse lookup matches.
		msg1Repeat := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 11, true, "alpha.unknown.example.", dnsmessage.TypeA, dnsmessage.ClassINET)))
		if got := extractReplyAddrs(t, msg1Repeat); !slices.Equal(got, addrs1) {
			t.Fatalf("unknown=%s repeat synthetic A = %v, want %v", unk, got, addrs1)
		}
		if revHost, ok := env.srv.SyntheticHost(addrs1[0]); !ok || revHost != "alpha.unknown.example" {
			t.Fatalf("SyntheticHost(%s) = %q, %v; want alpha.unknown.example, true", addrs1[0], revHost, ok)
		}

		// Second host gets distinct 198.18.0.3; AAAA gets NOERROR with 0 answers.
		msg2 := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 12, true, "beta.unknown.example.", dnsmessage.TypeA, dnsmessage.ClassINET)))
		if got := extractReplyAddrs(t, msg2); len(got) != 1 || got[0] != netip.MustParseAddr("198.18.0.3") {
			t.Fatalf("unknown=%s second synthetic A = %v, want [198.18.0.3]", unk, got)
		}
		msgV6 := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 13, true, "alpha.unknown.example.", dnsmessage.TypeAAAA, dnsmessage.ClassINET)))
		if msgV6.Header.RCode != dnsmessage.RCodeSuccess || len(msgV6.Answers) != 0 {
			t.Fatalf("synthetic AAAA = rcode %v answers %d, want NOERROR 0", msgV6.Header.RCode, len(msgV6.Answers))
		}

		// Explicit layer deny and locked deny still return NXDOMAIN under ask/allow/classify.
		for _, denied := range []string{"explicit-deny.example.com.", "locked-deny.example.com."} {
			dm := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 14, true, denied, dnsmessage.TypeA, dnsmessage.ClassINET)))
			if dm.Header.RCode != dnsmessage.RCodeNameError || len(dm.Answers) != 0 {
				t.Fatalf("unknown=%s explicit deny %s = %v, want NXDOMAIN", unk, denied, dm.Header.RCode)
			}
		}
	}

	// Policy update: switch Unknown to deny and explicitly deny alpha.unknown.example.
	// Existing table survives, alpha gets NXDOMAIN, granted registry.npmjs.org keeps working,
	// and a re-allowed alpha gets its original 198.18.0.2 without address reuse!
	polTight, _, err := MergeLayers([]api.Layer{{
		Kind:    api.LayerSession,
		Preset:  api.PresetBalanced,
		Unknown: api.UnknownDeny,
		Rules:   []api.Rule{{Allow: false, Door: api.DoorNet, Target: "alpha.unknown.example"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: polTight})

	if revHost, ok := env.srv.SyntheticHost(netip.MustParseAddr("198.18.0.2")); !ok || revHost != "alpha.unknown.example" {
		t.Fatalf("after CtlPolicyUpdate, SyntheticHost(198.18.0.2) = %q, %v", revHost, ok)
	}
	for _, blockedNow := range []string{"alpha.unknown.example.", "beta.unknown.example.", "gamma.unknown.example."} {
		dm := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 20, true, blockedNow, dnsmessage.TypeA, dnsmessage.ClassINET)))
		if dm.Header.RCode != dnsmessage.RCodeNameError {
			t.Fatalf("under UnknownDeny, %s = %v, want NXDOMAIN", blockedNow, dm.Header.RCode)
		}
	}
	// Preset-allowed registry.npmjs.org gets fresh 198.18.0.4 (never reusing .0.2 or .0.3!).
	msgNPM := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 21, true, "registry.npmjs.org.", dnsmessage.TypeA, dnsmessage.ClassINET)))
	if got := extractReplyAddrs(t, msgNPM); len(got) != 1 || got[0] != netip.MustParseAddr("198.18.0.4") {
		t.Fatalf("registry.npmjs.org synthetic A = %v, want [198.18.0.4]", got)
	}

	// Direct flow to synthetic 198.18.0.4 without numeric grant is still refused (metadata only).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stSynthFlow, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "198.18.0.4", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stSynthFlow, "CONNECT 198.18.0.4:443 HTTP/1.1\r\nHost: 198.18.0.4:443\r\n\r\n")
	respFlow, err := http.ReadResponse(bufio.NewReader(stSynthFlow), nil)
	if err != nil || respFlow.StatusCode != http.StatusForbidden {
		t.Fatalf("direct CONNECT to synthetic 198.18.0.4 = %v, %v; want 403 Forbidden", respFlow, err)
	}
	_ = respFlow.Body.Close()
	_ = stSynthFlow.Close()

	// Exhaustion of MaxSyntheticNames (8192) returns SERVFAIL with 0 upstream calls and no eviction.
	polFlood, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Preset: api.PresetBalanced, Unknown: api.UnknownAsk}})
	if err != nil {
		t.Fatal(err)
	}
	env = startTestServer(t, &Dialer{Lookup: fr.lookup})
	env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", DNS: DNSModeSynthetic, Policy: polFlood})
	var firstAssigned netip.Addr
	for i := range MaxSyntheticNames {
		name := fmt.Sprintf("h-%d.flood.example.", i)
		raw := buildSingleDNSQuery(t, 30, true, name, dnsmessage.TypeA, dnsmessage.ClassINET)
		msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), raw))
		addrs := extractReplyAddrs(t, msg)
		if msg.Header.RCode != dnsmessage.RCodeSuccess || len(addrs) != 1 {
			t.Fatalf("allocation %d failed early: rcode=%v addrs=%v", i, msg.Header.RCode, addrs)
		}
		if addrs[0] == netip.MustParseAddr("198.18.0.0") || addrs[0] == SyntheticDummyAddr || addrs[0] == netip.MustParseAddr("198.19.255.255") {
			t.Fatalf("allocated excluded boundary address %s", addrs[0])
		}
		if i == 0 {
			firstAssigned = addrs[0]
		}
	}
	msgOver := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 31, true, "overflow.flood.example.", dnsmessage.TypeA, dnsmessage.ClassINET)))
	if msgOver.Header.RCode != dnsmessage.RCodeServerFailure || len(msgOver.Answers) != 0 {
		t.Fatalf("overflow synthetic query = rcode %v answers %d, want SERVFAIL 0", msgOver.Header.RCode, len(msgOver.Answers))
	}
	// First host remains mapped without eviction.
	msgStill := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 32, true, "h-0.flood.example.", dnsmessage.TypeA, dnsmessage.ClassINET)))
	if got := extractReplyAddrs(t, msgStill); len(got) != 1 || got[0] != firstAssigned {
		t.Fatalf("h-0 after exhaustion = %v, want [%s]", got, firstAssigned)
	}

	// Verify upper subnet boundary 198.19.255.254 stops before 198.19.255.255.
	tbl := newSyntheticTable(maxSyntheticPoolSize)
	tbl.next = syntheticLastUint32
	lastAddr, ok := tbl.assign("last.example")
	if !ok || lastAddr != netip.MustParseAddr("198.19.255.254") {
		t.Fatalf("last pool addr = %s, %v; want 198.19.255.254, true", lastAddr, ok)
	}
	if _, ok := tbl.assign("broadcast.example"); ok {
		t.Fatal("syntheticTable allocated 198.19.255.255 broadcast address")
	}

	if fr.calls.Load() != 0 {
		t.Fatalf("synthetic tests caused %d upstream calls, want 0", fr.calls.Load())
	}
}

func TestDNSHTTPSSVCBAndMalformedInputs(t *testing.T) {
	fr := newFakeResolver(func(_ context.Context, host string) ([]netip.Addr, error) {
		t.Errorf("unexpected upstream lookup on HTTPS/SVCB/malformed test for %q", host)
		return nil, errors.New("unexpected lookup")
	})
	var env *testEnv

	for _, mode := range []string{DNSModeReal, DNSModeSynthetic} {
		env = startTestServer(t, &Dialer{Lookup: fr.lookup})
		pol, _, err := MergeLayers([]api.Layer{{
			Kind:    api.LayerSession,
			Preset:  api.PresetBalanced,
			Unknown: api.UnknownAsk,
			Rules:   []api.Rule{{Allow: false, Door: api.DoorNet, Target: "denied.example.com"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", DNS: mode, Policy: pol})

		for _, qt := range []dnsmessage.Type{dnsmessage.TypeHTTPS, dnsmessage.TypeSVCB, dnsmessage.TypeTXT, dnsmessage.TypeMX, dnsmessage.TypeCNAME} {
			msgOK := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 40, true, "registry.npmjs.org.", qt, dnsmessage.ClassINET)))
			if msgOK.Header.RCode != dnsmessage.RCodeSuccess || len(msgOK.Answers) != 0 {
				t.Fatalf("mode=%s allowed %v = rcode %v answers %d, want NOERROR 0", mode, qt, msgOK.Header.RCode, len(msgOK.Answers))
			}
			msgDeny := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 41, true, "denied.example.com.", qt, dnsmessage.ClassINET)))
			if msgDeny.Header.RCode != dnsmessage.RCodeNameError || len(msgDeny.Answers) != 0 {
				t.Fatalf("mode=%s denied %v = rcode %v answers %d, want NXDOMAIN 0", mode, qt, msgDeny.Header.RCode, len(msgDeny.Answers))
			}
		}

		// IP-literal QNames return NXDOMAIN in both real and synthetic UnknownAsk modes.
		for _, ipQ := range []string{"127.0.0.1.", "169.254.169.254.", "203.0.113.7."} {
			msgIP := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), buildSingleDNSQuery(t, 42, true, ipQ, dnsmessage.TypeA, dnsmessage.ClassINET)))
			if msgIP.Header.RCode != dnsmessage.RCodeNameError || len(msgIP.Answers) != 0 {
				t.Fatalf("mode=%s IP QName %s = rcode %v answers %d, want NXDOMAIN 0", mode, ipQ, msgIP.Header.RCode, len(msgIP.Answers))
			}
		}
	}

	// Short, oversize, and QR=1 response packets are silently dropped.
	if got := env.srv.AnswerDNS(context.Background(), []byte{1, 2, 3}); got != nil {
		t.Fatalf("short packet returned %x, want nil", got)
	}
	if got := env.srv.AnswerDNS(context.Background(), make([]byte, maxDNSMessageBytes+1)); got != nil {
		t.Fatalf("oversize packet returned %d bytes, want nil", len(got))
	}
	respBitPkt := buildSingleDNSQuery(t, 43, true, "registry.npmjs.org.", dnsmessage.TypeA, dnsmessage.ClassINET)
	respBitPkt[2] |= 0x80
	if got := env.srv.AnswerDNS(context.Background(), respBitPkt); got != nil {
		t.Fatalf("QR=1 response packet returned %x, want nil", got)
	}

	// Non-zero OpCode -> NOTIMP; non-IN class -> REFUSED; multi-question / trailing / loop -> FORMERR.
	opPkt := buildSingleDNSQuery(t, 44, true, "registry.npmjs.org.", dnsmessage.TypeA, dnsmessage.ClassINET)
	opPkt[2] |= 1 << 3
	if msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), opPkt)); msg.Header.RCode != dnsmessage.RCodeNotImplemented {
		t.Fatalf("opcode=1 rcode = %v, want NOTIMP", msg.Header.RCode)
	}

	chPkt := buildSingleDNSQuery(t, 45, true, "version.bind.", dnsmessage.TypeTXT, dnsmessage.ClassCHAOS)
	if msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), chPkt)); msg.Header.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("ClassCHAOS rcode = %v, want REFUSED", msg.Header.RCode)
	}

	multiQ := buildDNSQueryPacket(t, dnsmessage.Header{ID: 46}, []dnsmessage.Question{
		{Name: dnsmessage.MustNewName("registry.npmjs.org."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
	})
	trailing := append(buildSingleDNSQuery(t, 47, true, "registry.npmjs.org.", dnsmessage.TypeA, dnsmessage.ClassINET), 0x00)
	loopPkt := make([]byte, 18)
	binary.BigEndian.PutUint16(loopPkt[0:2], 48)
	binary.BigEndian.PutUint16(loopPkt[4:6], 1)
	loopPkt[12], loopPkt[13] = 0xc0, 0x0c // self-pointer compression loop
	binary.BigEndian.PutUint16(loopPkt[14:16], uint16(dnsmessage.TypeA))
	binary.BigEndian.PutUint16(loopPkt[16:18], uint16(dnsmessage.ClassINET))

	for name, pkt := range map[string][]byte{"multiQ": multiQ, "trailing": trailing, "loop": loopPkt} {
		msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), pkt))
		if msg.Header.RCode != dnsmessage.RCodeFormatError || len(msg.Answers) != 0 {
			t.Fatalf("%s rcode = %v answers %d, want FORMERR 0", name, msg.Header.RCode, len(msg.Answers))
		}
	}

	// Valid EDNS0 OPT query succeeds without upstream on HTTPS.
	ednsHTTPS := buildEDNSQuery(t, "registry.npmjs.org.", dnsmessage.TypeHTTPS, ".", nil)
	if msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), ednsHTTPS)); msg.Header.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("EDNS0 HTTPS rcode = %v, want NOERROR", msg.Header.RCode)
	}

	if fr.calls.Load() != 0 {
		t.Fatalf("total upstream calls = %d, want 0", fr.calls.Load())
	}
}

func TestDNSAddressRefusalNumericPortGrantAndRebind(t *testing.T) {
	privateAddr := dnsPrivateAddressFixture(t)
	mixedPrivateAddr := privateAddr.Next()
	var rebindFlip atomic.Bool
	var dialAttempts atomic.Int64
	hostAddr := netip.MustParseAddr("198.51.100.77")

	fr := newFakeResolver(func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "loop.example.com":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "meta.example.com":
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
		case "synthrange.example.com":
			return []netip.Addr{netip.MustParseAddr("198.18.0.9")}, nil
		case "nat64priv.example.com":
			return []netip.Addr{netip.MustParseAddr("64:ff9b::a00:5")}, nil
		case "hostif.example.com":
			return []netip.Addr{hostAddr}, nil
		case "mixed.example.com":
			return []netip.Addr{netip.MustParseAddr("203.0.113.10"), mixedPrivateAddr}, nil
		case "priv8443.example.com":
			return []netip.Addr{privateAddr}, nil
		case "privmismatch.example.com":
			return []netip.Addr{privateAddr}, nil
		case "rebind.example.com":
			if rebindFlip.Load() {
				return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.88")}, nil
		default:
			return nil, errors.New("unknown test host")
		}
	})

	env := startTestServer(t, &Dialer{
		HostAddrs: []netip.Addr{hostAddr},
		Lookup:    fr.lookup,
		DialTCP: func(_ context.Context, ap netip.AddrPort) (net.Conn, error) {
			dialAttempts.Add(1)
			return nil, fmt.Errorf("unexpected dial to %s", ap)
		},
	})

	pol, _, err := MergeLayers([]api.Layer{{
		Kind:   api.LayerSession,
		Preset: api.PresetOpen,
		Rules: []api.Rule{
			{Allow: true, Door: api.DoorNet, Target: privateAddr.String() + ":8443"},
			{Allow: false, Door: api.DoorNet, Target: "privmismatch.example.com:8443"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", DNS: DNSModeReal, Policy: pol})

	for _, h := range []string{
		"loop.example.com.",
		"meta.example.com.",
		"synthrange.example.com.",
		"nat64priv.example.com.",
		"hostif.example.com.",
		"mixed.example.com.",
		"privmismatch.example.com.",
	} {
		qType := dnsmessage.TypeA
		if h == "nat64priv.example.com." {
			qType = dnsmessage.TypeAAAA
		}
		msg := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 60, true, h, qType, dnsmessage.ClassINET)))
		if msg.Header.RCode != dnsmessage.RCodeRefused || len(msg.Answers) != 0 {
			t.Errorf("%s resolved to refused address: got rcode=%v answers=%v, want REFUSED with 0 answers", h, msg.Header.RCode, extractReplyAddrs(t, msg))
		}
	}

	// The exact numeric exception opens the RFC1918 fixture only on its granted port.
	msgPrivOK := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 61, true, "priv8443.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)))
	if got := extractReplyAddrs(t, msgPrivOK); msgPrivOK.Header.RCode != dnsmessage.RCodeSuccess || !slices.Equal(got, []netip.Addr{privateAddr}) {
		t.Fatalf("priv8443.example.com = rcode %v addrs %v, want [%v]", msgPrivOK.Header.RCode, got, privateAddr)
	}

	// DNS rebind: initial DNS lookup succeeds with 203.0.113.88, then record flips
	// to 169.254.169.254 before CONNECT flow dial -> flow dial performs fresh check and refuses.
	msgRebind := unpackDNSReply(t, queryDNSStream(t, env.supSess, buildSingleDNSQuery(t, 62, true, "rebind.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)))
	if msgRebind.Header.RCode != dnsmessage.RCodeSuccess || len(msgRebind.Answers) != 1 {
		t.Fatalf("initial rebind DNS = %+v", msgRebind)
	}
	rebindFlip.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stFlow, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "rebind.example.com", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stFlow, "CONNECT rebind.example.com:443 HTTP/1.1\r\nHost: rebind.example.com:443\r\n\r\n")
	respFlow, err := http.ReadResponse(bufio.NewReader(stFlow), nil)
	if err != nil || respFlow.StatusCode != http.StatusForbidden {
		t.Fatalf("rebound CONNECT = %v, %v; want 403 Forbidden", respFlow, err)
	}
	_ = respFlow.Body.Close()
	_ = stFlow.Close()
	if dialAttempts.Load() != 0 {
		t.Fatalf("rebound address reached DialTCP %d times, want 0", dialAttempts.Load())
	}
}

func TestDNSStreamLifecycleHeadersAndPolicyUpdateRace(t *testing.T) {
	fr := newFakeResolver(func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "registry.npmjs.org" && host != "live.example.com" {
			t.Errorf("ungranted host %q reached resolver during lifecycle/race test", host)
		}
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
	})
	env := startTestServer(t, &Dialer{Lookup: fr.lookup})
	rawNPM := buildSingleDNSQuery(t, 70, true, "registry.npmjs.org.", dnsmessage.TypeA, dnsmessage.ClassINET)

	// 1. Before CtlInit: KindDNS stream closes with zero bytes and zero upstream calls.
	if got := queryDNSStream(t, env.supSess, rawNPM); len(got) != 0 || fr.calls.Load() != 0 {
		t.Fatalf("before init KindDNS = %x calls=%d, want empty and 0 calls", got, fr.calls.Load())
	}

	// 2. Invalid DNS mode in CtlInit fails with *proto.Error and Hint.
	polBase, _, err := MergeLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	badResp := callCtlRaw(t, env, CtlInit, InitArgs{SessionID: "abcd2345", DNS: "bogus", Policy: polBase})
	if badResp.OK || badResp.Error == nil || badResp.Error.Hint == "" {
		t.Fatalf("invalid DNS mode CtlInit = %+v, want error with Hint", badResp)
	}

	// 3. Initialised with empty DNS mode ("") still refuses KindDNS streams.
	env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", DNS: "", Policy: polBase})
	if got := queryDNSStream(t, env.supSess, rawNPM); len(got) != 0 || fr.calls.Load() != 0 {
		t.Fatalf("empty DNS mode KindDNS = %x calls=%d, want empty and 0 calls", got, fr.calls.Load())
	}

	// 4. ParseDNSStreamHeader accepts only {} and {"kind":"dns"}.
	for _, validHdr := range [][]byte{[]byte(`{}`), []byte(`{"kind":"dns"}`), []byte(` { "kind" : "dns" } `)} {
		if err := ParseDNSStreamHeader(validHdr); err != nil {
			t.Errorf("ParseDNSStreamHeader(%s) failed: %v", validHdr, err)
		}
	}
	for _, badHdr := range [][]byte{
		nil,
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`"dns"`),
		[]byte(`{"kind":"egress"}`),
		[]byte(`{"kind":"dns","mode":"synthetic"}`),
		[]byte(`{"mode":"real"}`),
		[]byte(`{}{}`),
		bytes.Repeat([]byte(" "), maxDNSStreamHeader+1),
	} {
		if err := ParseDNSStreamHeader(badHdr); err == nil {
			t.Errorf("ParseDNSStreamHeader(%q) succeeded, want error", badHdr)
		}
	}

	// 5. DNS mode belongs to one egress lifetime. A fresh server uses real
	// mode while concurrent queries race with CtlPolicyUpdate.
	_ = env.srv.Close()
	env = startTestServer(t, &Dialer{Lookup: fr.lookup})
	env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", DNS: DNSModeReal, Policy: polBase})
	polWithLive, _, err := MergeLayers([]api.Layer{{
		Kind:   api.LayerSession,
		Preset: api.PresetBalanced,
		Rules:  []api.Rule{{Allow: true, Door: api.DoorNet, Target: "live.example.com"}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				qAllowed := buildSingleDNSQuery(t, 71, true, "live.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
				if resp := queryDNSStream(t, env.supSess, qAllowed); len(resp) > 0 {
					msg := unpackDNSReply(t, resp)
					if msg.Header.RCode != dnsmessage.RCodeSuccess && msg.Header.RCode != dnsmessage.RCodeNameError {
						t.Errorf("unexpected race rcode %v", msg.Header.RCode)
					}
				}
				qDenied := buildSingleDNSQuery(t, 72, true, "never-allowed.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
				if resp := queryDNSStream(t, env.supSess, qDenied); len(resp) > 0 {
					if msg := unpackDNSReply(t, resp); msg.Header.RCode != dnsmessage.RCodeNameError {
						t.Errorf("never-allowed rcode = %v, want NXDOMAIN", msg.Header.RCode)
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			if i%2 == 0 {
				env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: polWithLive})
			} else {
				env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: polBase})
			}
		}
	}()
	wg.Wait()

	// 6. After Server.Close(), new KindDNS opens fail closed.
	_ = env.srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if st, err := env.supSess.Open(ctx, link.KindDNS, nil); err == nil {
		_, _ = st.Write(rawNPM)
		_ = st.CloseWrite()
		got, _ := io.ReadAll(st)
		_ = st.Close()
		if len(got) != 0 {
			t.Fatalf("after Close(), KindDNS returned %x", got)
		}
	}
}
