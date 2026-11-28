package egress

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func buildTestDNSQuery(t *testing.T, id uint16, name string, qtype dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parseDNSResponse(t *testing.T, raw []byte) (dnsmessage.Header, []dnsmessage.Resource) {
	t.Helper()
	var p dnsmessage.Parser
	hdr, err := p.Start(raw)
	if err != nil {
		t.Fatalf("parse DNS response: %v", err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatalf("skip questions: %v", err)
	}
	ans, err := p.AllAnswers()
	if err != nil {
		t.Fatalf("read answers: %v", err)
	}
	return hdr, ans
}

type countingResolver struct {
	calls atomic.Int32
	ips   map[string][]netip.Addr
}

func (r *countingResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.calls.Add(1)
	return r.ips[host], nil
}

func TestDNSRealModePolicyAndSuppression(t *testing.T) {
	eng, err := NewEngine(Config{
		Agent:   "claude",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
		Network: NetworkConfig{
			Allow: []string{"rebind-private.example"},
			Deny:  []string{"github.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	res := &countingResolver{ips: map[string][]netip.Addr{
		"registry.npmjs.org":     {ip4(104, 16, 2, 34), netip.MustParseAddr("2606:4700::6810:222")},
		"rebind-private.example": {ip4(169, 254, 169, 254)},
	}}
	sd := &SafeDialer{Addrs: ap, Engine: eng, Resolver: res}
	h := NewDNSHandler(DNSOptions{Engine: eng, Addrs: ap, Dialer: sd, Mode: DNSModeReal})

	// 1. Disallowed host gets NXDOMAIN with ZERO upstream lookups.
	q := buildTestDNSQuery(t, 1, "example.com.", dnsmessage.TypeA)
	rawResp, err := h.Handle(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	hdr, ans := parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeNameError || len(ans) != 0 {
		t.Errorf("disallowed host: rcode=%v answers=%d, want NXDOMAIN and 0 answers", hdr.RCode, len(ans))
	}
	if res.calls.Load() != 0 {
		t.Errorf("upstream resolver called %d times for disallowed host, want 0", res.calls.Load())
	}

	// 2. Denied host overriding balanced preset gets NXDOMAIN with ZERO upstream lookups.
	q = buildTestDNSQuery(t, 2, "github.com.", dnsmessage.TypeA)
	rawResp, err = h.Handle(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ = parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeNameError || res.calls.Load() != 0 {
		t.Errorf("denied host: rcode=%v calls=%d, want NXDOMAIN and 0 calls", hdr.RCode, res.calls.Load())
	}

	// 3. HTTPS (65) and SVCB (64) get empty NOERROR with ZERO upstream lookups.
	for _, qt := range []dnsmessage.Type{typeHTTPS, typeSVCB} {
		q = buildTestDNSQuery(t, 3, "registry.npmjs.org.", qt)
		rawResp, err = h.Handle(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		hdr, ans = parseDNSResponse(t, rawResp)
		if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 0 || res.calls.Load() != 0 {
			t.Errorf("type %d: rcode=%v ans=%d calls=%d, want empty NOERROR and 0 calls", qt, hdr.RCode, len(ans), res.calls.Load())
		}
	}

	// 4. TXT query on allowed name gets empty NOERROR with ZERO upstream lookups.
	q = buildTestDNSQuery(t, 4, "registry.npmjs.org.", dnsmessage.TypeTXT)
	rawResp, err = h.Handle(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	hdr, ans = parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 0 || res.calls.Load() != 0 {
		t.Errorf("TXT: rcode=%v ans=%d calls=%d, want empty NOERROR and 0 calls", hdr.RCode, len(ans), res.calls.Load())
	}

	// 5. Allowed host resolves real A and AAAA records.
	q = buildTestDNSQuery(t, 5, "registry.npmjs.org.", dnsmessage.TypeA)
	rawResp, err = h.Handle(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	hdr, ans = parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 1 {
		t.Fatalf("allowed A: rcode=%v ans=%d, want NOERROR and 1 answer", hdr.RCode, len(ans))
	}
	aBody, ok := ans[0].Body.(*dnsmessage.AResource)
	if !ok || netip.AddrFrom4(aBody.A) != ip4(104, 16, 2, 34) {
		t.Errorf("A answer = %+v, want 104.16.2.34", ans[0].Body)
	}

	q = buildTestDNSQuery(t, 6, "registry.npmjs.org.", dnsmessage.TypeAAAA)
	rawResp, err = h.Handle(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	hdr, ans = parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 1 {
		t.Fatalf("allowed AAAA: rcode=%v ans=%d, want NOERROR and 1 answer", hdr.RCode, len(ans))
	}

	// 6. Allowed host that resolves to metadata IP is refused (NXDOMAIN).
	q = buildTestDNSQuery(t, 7, "rebind-private.example.", dnsmessage.TypeA)
	rawResp, err = h.Handle(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	hdr, ans = parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeNameError || len(ans) != 0 {
		t.Errorf("metadata-resolving host: rcode=%v ans=%d, want NXDOMAIN", hdr.RCode, len(ans))
	}
}

func TestDNSSyntheticMode(t *testing.T) {
	eng, err := NewEngine(Config{
		Sandbox: SandboxConfig{Preset: PresetBalanced},
		Network: NetworkConfig{Unknown: UnknownDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := &countingResolver{}
	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := NewDNSHandler(DNSOptions{
		Engine: eng,
		Addrs:  ap,
		Dialer: &SafeDialer{Addrs: ap, Engine: eng, Resolver: res},
		Mode:   DNSModeSynthetic,
	})

	// Unknown host under unknown=deny gets NXDOMAIN with zero upstream queries.
	rawResp, err := h.Handle(context.Background(), buildTestDNSQuery(t, 10, "unknown.example.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ := parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeNameError {
		t.Errorf("synthetic unknown=deny: rcode=%v, want NXDOMAIN", hdr.RCode)
	}

	// Allowed host gets a stable synthetic IP in 198.18.0.0/15 without querying upstream.
	rawResp, err = h.Handle(context.Background(), buildTestDNSQuery(t, 11, "registry.npmjs.org.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	hdr, ans := parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 1 {
		t.Fatalf("synthetic allowed: rcode=%v ans=%d", hdr.RCode, len(ans))
	}
	synthIP := netip.AddrFrom4(ans[0].Body.(*dnsmessage.AResource).A)
	synthPrefix := netip.MustParsePrefix("198.18.0.0/15")
	if !synthPrefix.Contains(synthIP) {
		t.Errorf("synthetic IP %s outside 198.18.0.0/15", synthIP)
	}
	if mapped, ok := h.LookupSynthetic(synthIP); !ok || mapped != "registry.npmjs.org" {
		t.Errorf("LookupSynthetic(%s) = %q, %v; want registry.npmjs.org", synthIP, mapped, ok)
	}
	if res.calls.Load() != 0 {
		t.Errorf("synthetic mode called upstream resolver %d times, want 0", res.calls.Load())
	}

	// Under unknown=allow, unknown name gets a synthetic IP with zero upstream queries.
	if err := eng.Reload(Config{
		Sandbox: SandboxConfig{Preset: PresetLocked},
		Network: NetworkConfig{Unknown: UnknownAllow},
	}); err != nil {
		t.Fatal(err)
	}
	rawResp, err = h.Handle(context.Background(), buildTestDNSQuery(t, 12, "newsite.example.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	hdr, ans = parseDNSResponse(t, rawResp)
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 1 || res.calls.Load() != 0 {
		t.Errorf("synthetic unknown=allow: rcode=%v ans=%d calls=%d", hdr.RCode, len(ans), res.calls.Load())
	}
}

func FuzzDNSHandle(f *testing.F) {
	eng, _ := NewEngine(Config{Sandbox: SandboxConfig{Preset: PresetBalanced}})
	ap, _ := NewAddressPolicyWithHosts(nil)
	h := NewDNSHandler(DNSOptions{
		Engine: eng,
		Addrs:  ap,
		Dialer: &SafeDialer{Addrs: ap, Engine: eng, Resolver: &countingResolver{}},
		Mode:   DNSModeSynthetic,
	})
	f.Add([]byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1})
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff})
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = h.Handle(context.Background(), data)
	})
}
