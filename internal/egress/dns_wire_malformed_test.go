package egress

import (
	"encoding/binary"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestParseDNSQueryRefusesMalformedPackets(t *testing.T) {
	validA := buildSingleDNSQuery(t, 0x1111, true, "example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
	validEDNS := buildEDNSQuery(t, "example.com.", dnsmessage.TypeA, ".", nil)

	buildExtra := func(fn func(*dnsmessage.Builder)) []byte {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 1})
		_ = b.StartQuestions()
		_ = b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
		fn(&b)
		pkt, _ := b.Finish()
		return pkt
	}
	withAnswer := buildExtra(func(b *dnsmessage.Builder) {
		_ = b.StartAnswers()
		_ = b.AResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}})
	})
	withAuthority := buildExtra(func(b *dnsmessage.Builder) {
		_ = b.StartAuthorities()
		_ = b.NSResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeNS, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.NSResource{NS: dnsmessage.MustNewName("ns.example.com.")})
	})
	withTXTAdditional := buildExtra(func(b *dnsmessage.Builder) {
		_ = b.StartAdditionals()
		_ = b.TXTResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("."), Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET}, dnsmessage.TXTResource{TXT: []string{"x"}})
	})
	withTwoOPTs := buildExtra(func(b *dnsmessage.Builder) {
		_ = b.StartAdditionals()
		optHdr := dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("."), Type: dnsmessage.TypeOPT, Class: 1232}
		_ = b.OPTResource(optHdr, dnsmessage.OPTResource{})
		_ = b.OPTResource(optHdr, dnsmessage.OPTResource{})
	})

	maxCountsHdr := make([]byte, dnsHeaderBytes)
	binary.BigEndian.PutUint16(maxCountsHdr[4:6], 0xffff)
	binary.BigEndian.PutUint16(maxCountsHdr[6:8], 0xffff)
	binary.BigEndian.PutUint16(maxCountsHdr[8:10], 0xffff)
	binary.BigEndian.PutUint16(maxCountsHdr[10:12], 0xffff)

	badOPTRData := slices.Clone(validA)
	badOPTRData[11] = 1
	badOPTRData = append(badOPTRData, 0, 0, 41, 0x04, 0xd0, 0, 0, 0, 0, 0, 4, 0, 10, 0, 8)

	badCases := []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"short 11-byte header", validA[:11]},
		{"exceeds 65535-byte protocol cap", make([]byte, maxDNSMessageBytes+1)},
		{"untrusted 0xffff counts", maxCountsHdr},
		{"QR response flag set", buildDNSQueryPacket(t, dnsmessage.Header{ID: 1, Response: true}, []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}})},
		{"TC truncated flag set", buildDNSQueryPacket(t, dnsmessage.Header{ID: 1, Truncated: true}, []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}})},
		{"non-query opcode IQUERY", buildDNSQueryPacket(t, dnsmessage.Header{ID: 1, OpCode: 1}, []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}})},
		{"non-query opcode UPDATE", buildDNSQueryPacket(t, dnsmessage.Header{ID: 1, OpCode: 5}, []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}})},
		{"zero questions", buildDNSQueryPacket(t, dnsmessage.Header{ID: 1}, nil)},
		{"multiple questions", buildDNSQueryPacket(t, dnsmessage.Header{ID: 1}, []dnsmessage.Question{
			{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
			{Name: dnsmessage.MustNewName("example.org."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		})},
		{"CHAOS question class", buildSingleDNSQuery(t, 1, true, "example.com.", dnsmessage.TypeA, dnsmessage.ClassCHAOS)},
		{"ANY question class", buildSingleDNSQuery(t, 1, true, "example.com.", dnsmessage.TypeA, dnsmessage.ClassANY)},
		{"existing answer record", withAnswer},
		{"existing authority record", withAuthority},
		{"non-OPT TXT additional", withTXTAdditional},
		{"multiple OPT additionals", withTwoOPTs},
		{"non-root OPT owner", buildEDNSQuery(t, "example.com.", dnsmessage.TypeA, "example.com.", nil)},
		{"malformed OPT option length", badOPTRData},
		{"trailing byte after query", slices.Concat(validA, []byte{0x00})},
		{"trailing bytes after EDNS query", slices.Concat(validEDNS, []byte{0xde, 0xad})},
		{"concatenated second DNS query", slices.Concat(validA, validA)},
		{"truncated mid-label", validA[:16]},
		{"truncated mid-qclass", validA[:len(validA)-1]},
		{"truncated EDNS OPT header", validEDNS[:len(validEDNS)-4]},
		{"self compression loop", []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x0c, 0, 1, 0, 1}},
		{"prefix compression loop", []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 'a', 0xc0, 0x0c, 0, 1, 0, 1}},
		{"mutual compression loop", []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x12, 0, 1, 0, 1, 0xc0, 0x0c}},
		{"compression pointer into header", []byte{0, 0, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x00, 0, 1, 0, 1}},
		{"forward compression pointer", []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x12, 0, 1, 0, 1, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0}},
		{"truncated compression pointer", []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0}},
		{"extended label prefix", []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0x41, 'a', 0, 0, 1, 0, 1}},
		{"literal dot inside single wire label", []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 11, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm', 0, 0, 1, 0, 1}},
		{"root domain query", buildSingleDNSQuery(t, 1, true, ".", dnsmessage.TypeA, dnsmessage.ClassINET)},
		{"underscore in hostname", buildSingleDNSQuery(t, 1, true, "_443._tcp.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)},
		{"leading hyphen in label", buildSingleDNSQuery(t, 1, true, "-bad.example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)},
		{"numeric final label", buildSingleDNSQuery(t, 1, true, "203.0.113.10.", dnsmessage.TypeA, dnsmessage.ClassINET)},
	}

	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseDNSQuery(tc.raw); err == nil {
				t.Fatalf("parseDNSQuery(%s) succeeded with %+v, want error", tc.name, got)
			}
		})
	}
}

func TestPackDNSResponse65KBoundAndInvalidInput(t *testing.T) {
	if _, err := packDNSResponse(dnsQuery{}, dnsmessage.RCodeSuccess, nil); err == nil {
		t.Fatal("zero dnsQuery was accepted")
	}

	raw := buildSingleDNSQuery(t, 0x2222, true, "a.co.", dnsmessage.TypeAAAA, dnsmessage.ClassINET)
	q, err := parseDNSQuery(raw)
	if err != nil {
		t.Fatal(err)
	}

	// 2339 compressed AAAA answers fit just below 65535 bytes (22 + 2339*28 = 65514).
	fitV6 := make([]netip.Addr, 2339)
	for i := range fitV6 {
		var b [16]byte
		b[0], b[1], b[2], b[3] = 0x20, 0x01, 0x0d, 0xb8
		binary.BigEndian.PutUint32(b[12:16], uint32(i+1))
		fitV6[i] = netip.AddrFrom16(b)
	}
	okPkt, err := packDNSResponse(q, dnsmessage.RCodeSuccess, fitV6)
	if err != nil || len(okPkt) > maxDNSMessageBytes {
		t.Fatalf("near-limit AAAA response = len %d, err %v", len(okPkt), err)
	}

	// 2340 compressed AAAA answers cross 65535 bytes (22 + 2340*28 = 65542 > 65535).
	overV6 := slices.Concat(fitV6, []netip.Addr{netip.MustParseAddr("2001:db8::ffff:ffff")})
	if _, err := packDNSResponse(q, dnsmessage.RCodeSuccess, overV6); err == nil {
		t.Fatal("response over 65535 bytes was accepted")
	}

	rawA := buildSingleDNSQuery(t, 0x2223, true, strings.Repeat("a", 63)+".co.", dnsmessage.TypeA, dnsmessage.ClassINET)
	qA, err := parseDNSQuery(rawA)
	if err != nil {
		t.Fatal(err)
	}
	hugeV4 := make([]netip.Addr, maxDNSAnswers+10)
	for i := range hugeV4 {
		hugeV4[i] = netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})
	}
	if _, err := packDNSResponse(qA, dnsmessage.RCodeSuccess, hugeV4); err == nil {
		t.Fatal("IPv4 response exceeding 65535 bytes was accepted")
	}
}
