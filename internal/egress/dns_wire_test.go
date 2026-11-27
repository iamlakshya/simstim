package egress

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func buildDNSQueryPacket(tb testing.TB, hdr dnsmessage.Header, questions []dnsmessage.Question) []byte {
	tb.Helper()
	b := dnsmessage.NewBuilder(nil, hdr)
	if err := b.StartQuestions(); err != nil {
		tb.Fatal(err)
	}
	for _, q := range questions {
		if err := b.Question(q); err != nil {
			tb.Fatal(err)
		}
	}
	pkt, err := b.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	return pkt
}

func buildSingleDNSQuery(tb testing.TB, id uint16, rd bool, qname string, qtype dnsmessage.Type, qclass dnsmessage.Class) []byte {
	tb.Helper()
	return buildDNSQueryPacket(tb, dnsmessage.Header{ID: id, RecursionDesired: rd}, []dnsmessage.Question{{
		Name:  dnsmessage.MustNewName(qname),
		Type:  qtype,
		Class: qclass,
	}})
}

func buildEDNSQuery(tb testing.TB, qname string, qtype dnsmessage.Type, optOwner string, options []dnsmessage.Option) []byte {
	tb.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x3141, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		tb.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(qname),
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		tb.Fatal(err)
	}
	if err := b.StartAdditionals(); err != nil {
		tb.Fatal(err)
	}
	if err := b.OPTResource(dnsmessage.ResourceHeader{
		Name:  dnsmessage.MustNewName(optOwner),
		Type:  dnsmessage.TypeOPT,
		Class: 1232,
	}, dnsmessage.OPTResource{Options: options}); err != nil {
		tb.Fatal(err)
	}
	pkt, err := b.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	return pkt
}

func TestParseDNSQueryCanonicalisationAndEDNS(t *testing.T) {
	cases := []struct {
		name     string
		id       uint16
		rd       bool
		qname    string
		qtype    dnsmessage.Type
		wantHost string
	}{
		{"mixed case and trailing dot", 0x1234, true, "Api.Example.COM.", dnsmessage.TypeA, "api.example.com"},
		{"AAAA with recursion disabled", 0xbeef, false, "Sub-1.Registry.NPMJS.org.", dnsmessage.TypeAAAA, "sub-1.registry.npmjs.org"},
		{"HTTPS query", 0x0042, true, "CDN.Example.Invalid.", dnsmessage.TypeHTTPS, "cdn.example.invalid"},
		{"SVCB query", 0x0043, true, "SVC.Example.COM.", dnsmessage.TypeSVCB, "svc.example.com"},
		{"single label hostname", 0x0099, true, "LocalHost.", dnsmessage.TypeA, "localhost"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := buildSingleDNSQuery(t, tc.id, tc.rd, tc.qname, tc.qtype, dnsmessage.ClassINET)
			got, err := parseDNSQuery(raw)
			if err != nil {
				t.Fatalf("parseDNSQuery(%q) failed: %v", tc.qname, err)
			}
			if got.host != tc.wantHost {
				t.Fatalf("host = %q, want %q", got.host, tc.wantHost)
			}
			if got.header.ID != tc.id || got.header.RecursionDesired != tc.rd || got.header.Response {
				t.Fatalf("header = %+v", got.header)
			}
			if got.question.Name.String() != tc.qname || got.question.Type != tc.qtype || got.question.Class != dnsmessage.ClassINET {
				t.Fatalf("question = %+v, want echoed %q %v", got.question, tc.qname, tc.qtype)
			}
		})
	}

	ednsEmpty := buildEDNSQuery(t, "Example.COM.", dnsmessage.TypeA, ".", nil)
	if q, err := parseDNSQuery(ednsEmpty); err != nil || q.host != "example.com" {
		t.Fatalf("EDNS empty OPT = %+v, %v", q, err)
	}

	ednsOpt := buildEDNSQuery(t, "Example.COM.", dnsmessage.TypeAAAA, ".", []dnsmessage.Option{
		{Code: 10, Data: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
	})
	if q, err := parseDNSQuery(ednsOpt); err != nil || q.host != "example.com" {
		t.Fatalf("EDNS cookie OPT = %+v, %v", q, err)
	}

	// Valid backwards compression pointer in OPT owner pointing to QNAME's root 0x00 at offset 24.
	compOPT := slices.Clone(buildSingleDNSQuery(t, 0x7788, true, "example.com.", dnsmessage.TypeA, dnsmessage.ClassINET))
	compOPT[11] = 1
	compOPT = append(compOPT, 0xc0, 24, 0x00, 41, 0x04, 0xd0, 0, 0, 0, 0, 0, 0)
	if qComp, err := parseDNSQuery(compOPT); err != nil || qComp.host != "example.com" {
		t.Fatalf("compressed root OPT = %+v, %v", qComp, err)
	}
}

func TestPackDNSResponseFamiliesQuestionPreservationAndEmptySVCBHTTPS(t *testing.T) {
	if !isSuppressedSVCBType(dnsmessage.TypeHTTPS) || !isSuppressedSVCBType(dnsmessage.TypeSVCB) || isSuppressedSVCBType(dnsmessage.TypeA) {
		t.Fatal("unexpected isSuppressedSVCBType classification")
	}

	mixedAddrs := []netip.Addr{
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("203.0.113.25"),
		netip.MustParseAddr("2001:db8::2"),
		netip.MustParseAddr("::ffff:203.0.113.99"),
		netip.MustParseAddr("fe80::1%eth0"),
		{},
	}

	// 1. TypeA emits only pure IPv4 answers and preserves mixed-case question & ID/RD.
	rawA := buildSingleDNSQuery(t, 0x4a2b, true, "Mixed.Example.COM.", dnsmessage.TypeA, dnsmessage.ClassINET)
	qA, err := parseDNSQuery(rawA)
	if err != nil {
		t.Fatal(err)
	}
	respABytes, err := packDNSResponse(qA, dnsmessage.RCodeSuccess, mixedAddrs)
	if err != nil {
		t.Fatal(err)
	}
	var pA dnsmessage.Parser
	hdrA, err := pA.Start(respABytes)
	if err != nil {
		t.Fatal(err)
	}
	if hdrA.ID != 0x4a2b || !hdrA.Response || !hdrA.RecursionDesired || !hdrA.RecursionAvailable || hdrA.RCode != dnsmessage.RCodeSuccess || hdrA.Authoritative || hdrA.AuthenticData {
		t.Fatalf("TypeA response header = %+v", hdrA)
	}
	if qs, err := pA.AllQuestions(); err != nil || len(qs) != 1 || qs[0] != qA.question {
		t.Fatalf("TypeA echoed questions = %+v, %v; want %+v", qs, err, qA.question)
	}
	var gotV4 []netip.Addr
	for {
		ah, err := pA.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ah.Type != dnsmessage.TypeA || ah.Class != dnsmessage.ClassINET || ah.Name != qA.question.Name || ah.TTL != 30 {
			t.Fatalf("unexpected A answer header: %+v", ah)
		}
		rec, err := pA.AResource()
		if err != nil {
			t.Fatal(err)
		}
		gotV4 = append(gotV4, netip.AddrFrom4(rec.A))
	}
	if len(gotV4) != 2 || gotV4[0] != netip.MustParseAddr("192.0.2.10") || gotV4[1] != netip.MustParseAddr("203.0.113.25") {
		t.Fatalf("TypeA answers = %v, want [192.0.2.10 203.0.113.25]", gotV4)
	}
	if _, err := pA.AuthorityHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		t.Fatalf("unexpected authorities in TypeA response: %v", err)
	}
	if _, err := pA.AdditionalHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		t.Fatalf("unexpected additionals in TypeA response: %v", err)
	}

	// 2. TypeAAAA emits only pure IPv6 answers (never IPv4 or ::ffff:v4) and preserves RD=false.
	rawAAAA := buildSingleDNSQuery(t, 0x9102, false, "Mixed.Example.COM.", dnsmessage.TypeAAAA, dnsmessage.ClassINET)
	qAAAA, err := parseDNSQuery(rawAAAA)
	if err != nil {
		t.Fatal(err)
	}
	respAAAABytes, err := packDNSResponse(qAAAA, dnsmessage.RCodeSuccess, mixedAddrs)
	if err != nil {
		t.Fatal(err)
	}
	var pAAAA dnsmessage.Parser
	hdrAAAA, err := pAAAA.Start(respAAAABytes)
	if err != nil {
		t.Fatal(err)
	}
	if hdrAAAA.ID != 0x9102 || !hdrAAAA.Response || hdrAAAA.RecursionDesired || !hdrAAAA.RecursionAvailable || hdrAAAA.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("TypeAAAA response header = %+v", hdrAAAA)
	}
	if qs, err := pAAAA.AllQuestions(); err != nil || len(qs) != 1 || qs[0] != qAAAA.question {
		t.Fatalf("TypeAAAA echoed questions = %+v, %v", qs, err)
	}
	var gotV6 []netip.Addr
	for {
		ah, err := pAAAA.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ah.Type != dnsmessage.TypeAAAA || ah.Class != dnsmessage.ClassINET || ah.Name != qAAAA.question.Name {
			t.Fatalf("unexpected AAAA answer header: %+v", ah)
		}
		rec, err := pAAAA.AAAAResource()
		if err != nil {
			t.Fatal(err)
		}
		gotV6 = append(gotV6, netip.AddrFrom16(rec.AAAA))
	}
	if len(gotV6) != 2 || gotV6[0] != netip.MustParseAddr("2001:db8::1") || gotV6[1] != netip.MustParseAddr("2001:db8::2") {
		t.Fatalf("TypeAAAA answers = %v, want [2001:db8::1 2001:db8::2]", gotV6)
	}

	// 3. TypeHTTPS (65) and TypeSVCB (64), plus CNAME/NS/TXT, ALWAYS emit zero answers.
	for _, qt := range []dnsmessage.Type{
		dnsmessage.TypeHTTPS,
		dnsmessage.TypeSVCB,
		dnsmessage.TypeCNAME,
		dnsmessage.TypeNS,
		dnsmessage.TypeTXT,
	} {
		rawQ := buildSingleDNSQuery(t, 0x55aa, true, "ech.example.com.", qt, dnsmessage.ClassINET)
		parsedQ, err := parseDNSQuery(rawQ)
		if err != nil {
			t.Fatalf("parseDNSQuery(%v) failed: %v", qt, err)
		}
		pkt, err := packDNSResponse(parsedQ, dnsmessage.RCodeSuccess, mixedAddrs)
		if err != nil {
			t.Fatalf("packDNSResponse(%v) failed: %v", qt, err)
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(pkt); err != nil {
			t.Fatalf("Unpack(%v) failed: %v", qt, err)
		}
		if msg.Header.ID != 0x55aa || !msg.Header.Response || msg.Header.RCode != dnsmessage.RCodeSuccess {
			t.Fatalf("type %v header = %+v", qt, msg.Header)
		}
		if len(msg.Questions) != 1 || msg.Questions[0] != parsedQ.question {
			t.Fatalf("type %v questions = %+v", qt, msg.Questions)
		}
		if len(msg.Answers) != 0 || len(msg.Authorities) != 0 || len(msg.Additionals) != 0 {
			t.Fatalf("type %v returned non-empty sections: answers=%d auth=%d add=%d", qt, len(msg.Answers), len(msg.Authorities), len(msg.Additionals))
		}
	}

	// 4. Non-success RCodes omit answers even when addresses are supplied.
	for _, rc := range []dnsmessage.RCode{
		dnsmessage.RCodeRefused,
		dnsmessage.RCodeNameError,
		dnsmessage.RCodeServerFailure,
		dnsmessage.RCodeFormatError,
	} {
		pkt, err := packDNSResponse(qA, rc, mixedAddrs)
		if err != nil {
			t.Fatalf("packDNSResponse(%v) failed: %v", rc, err)
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(pkt); err != nil {
			t.Fatal(err)
		}
		if msg.Header.RCode != rc || len(msg.Answers) != 0 || len(msg.Questions) != 1 || msg.Questions[0] != qA.question {
			t.Fatalf("rcode %v unpacked = %+v", rc, msg)
		}
	}
}
