package egress

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func FuzzDNSWire(f *testing.F) {
	seeds := [][]byte{
		buildSingleDNSQuery(f, 0x0001, true, "Example.COM.", dnsmessage.TypeA, dnsmessage.ClassINET),
		buildSingleDNSQuery(f, 0x0002, false, "registry.npmjs.org.", dnsmessage.TypeAAAA, dnsmessage.ClassINET),
		buildSingleDNSQuery(f, 0x0003, true, "ech.example.com.", dnsmessage.TypeHTTPS, dnsmessage.ClassINET),
		buildSingleDNSQuery(f, 0x0004, true, "svc.example.com.", dnsmessage.TypeSVCB, dnsmessage.ClassINET),
		buildSingleDNSQuery(f, 0x0005, true, "txt.example.com.", dnsmessage.TypeTXT, dnsmessage.ClassINET),
		buildEDNSQuery(f, "edns.example.com.", dnsmessage.TypeA, ".", []dnsmessage.Option{{Code: 10, Data: []byte{1, 2, 3, 4}}}),
		{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x0c, 0, 1, 0, 1},
		{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 11, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm', 0, 0, 1, 0, 1},
		{},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	sampleAddrs := []netip.Addr{
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("2001:db8::1"),
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		q, err := parseDNSQuery(raw)
		if err != nil {
			return
		}
		if q.header.Response || q.header.OpCode != 0 || q.header.Truncated {
			t.Fatalf("invalid parsed query header: %+v", q.header)
		}
		if q.question.Class != dnsmessage.ClassINET {
			t.Fatalf("unexpected question class: %v", q.question.Class)
		}
		norm, err := NormaliseHost(q.host)
		if err != nil || norm != q.host || isNumericAddr(q.host) {
			t.Fatalf("parsed host %q is not canonical (%q, %v)", q.host, norm, err)
		}

		pkt, err := packDNSResponse(q, dnsmessage.RCodeSuccess, sampleAddrs)
		if err != nil {
			t.Fatalf("packDNSResponse failed for valid query %+v: %v", q, err)
		}
		if len(pkt) > maxDNSMessageBytes {
			t.Fatalf("packed response length %d > %d", len(pkt), maxDNSMessageBytes)
		}

		var resp dnsmessage.Message
		if err := resp.Unpack(pkt); err != nil {
			t.Fatalf("Unpack packed response failed: %v", err)
		}
		if resp.Header.ID != q.header.ID || !resp.Header.Response || resp.Header.RecursionDesired != q.header.RecursionDesired || !resp.Header.RecursionAvailable {
			t.Fatalf("unpacked response header mismatch: %+v vs query %+v", resp.Header, q.header)
		}
		if len(resp.Questions) != 1 || resp.Questions[0] != q.question {
			t.Fatalf("echoed question mismatch: got %+v, want %+v", resp.Questions, q.question)
		}
		wantAnswers := 0
		if q.question.Type == dnsmessage.TypeA || q.question.Type == dnsmessage.TypeAAAA {
			wantAnswers = 1
		}
		if len(resp.Answers) != wantAnswers {
			t.Fatalf("type %v answers = %d, want %d", q.question.Type, len(resp.Answers), wantAnswers)
		}
		if isSuppressedSVCBType(q.question.Type) && len(resp.Answers) != 0 {
			t.Fatalf("HTTPS/SVCB leaked %d answers", len(resp.Answers))
		}

		if _, err := parseDNSQuery(pkt); err == nil {
			t.Fatal("parseDNSQuery accepted a packed DNS response")
		}
	})
}
