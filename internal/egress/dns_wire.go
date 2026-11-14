package egress

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	dnsHeaderBytes            = 12
	maxDNSMessageBytes        = 65535
	maxDNSWireName            = 254 // label length and data octets before the root 0x00
	maxDNSAnswers             = maxDNSMessageBytes / 16
	maxDNSAAAAAnswers         = maxDNSMessageBytes / 28
	dnsAnswerTTL       uint32 = 30
)

var (
	errDNSQuery    = errors.New("invalid DNS query")
	errDNSResponse = errors.New("invalid DNS response")
)

// dnsQuery holds one validated standard IN DNS query and its canonicalised
// hostname from NormaliseHost.
type dnsQuery struct {
	header   dnsmessage.Header
	question dnsmessage.Question
	host     string
}

// parseDNSQuery validates one raw DNS QUERY packet with a single ClassINET
// question, unset QR and TC flags, no answers or authorities, at most one
// well-formed EDNS0 OPT additional record, exact message framing without
// trailing bytes, and a query name accepted by NormaliseHost.
func parseDNSQuery(raw []byte) (dnsQuery, error) {
	if len(raw) < dnsHeaderBytes {
		return dnsQuery{}, fmt.Errorf("%w: packet length %d shorter than %d-byte header", errDNSQuery, len(raw), dnsHeaderBytes)
	}
	if len(raw) > maxDNSMessageBytes {
		return dnsQuery{}, fmt.Errorf("%w: packet length %d exceeds %d-byte limit", errDNSQuery, len(raw), maxDNSMessageBytes)
	}

	var p dnsmessage.Parser
	hdr, err := p.Start(raw)
	if err != nil {
		return dnsQuery{}, fmt.Errorf("parse DNS header: %w", err)
	}
	if err := checkDNSQueryHeader(hdr, raw); err != nil {
		return dnsQuery{}, err
	}

	q, err := p.Question()
	if err != nil {
		return dnsQuery{}, fmt.Errorf("parse DNS question: %w", err)
	}
	if _, err := p.Question(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return dnsQuery{}, fmt.Errorf("%w: unexpected extra question", errDNSQuery)
	}
	if q.Class != dnsmessage.ClassINET {
		return dnsQuery{}, fmt.Errorf("%w: unsupported question class %v", errDNSQuery, q.Class)
	}

	arCount := binary.BigEndian.Uint16(raw[10:12])
	if err := checkDNSQuerySections(&p, arCount); err != nil {
		return dnsQuery{}, err
	}
	if err := checkDNSQueryFraming(raw, arCount); err != nil {
		return dnsQuery{}, err
	}

	rawName := q.Name.String()
	host, err := NormaliseHost(rawName)
	if err != nil {
		return dnsQuery{}, fmt.Errorf("normalise DNS query host %q: %w", rawName, err)
	}
	if isNumericAddr(host) {
		return dnsQuery{}, fmt.Errorf("%w: numeric IP literal %q in DNS query", errDNSQuery, rawName)
	}

	return dnsQuery{
		header:   hdr,
		question: q,
		host:     host,
	}, nil
}

func checkDNSQueryHeader(hdr dnsmessage.Header, raw []byte) error {
	if hdr.Response {
		return fmt.Errorf("%w: response flag set on query", errDNSQuery)
	}
	if hdr.OpCode != 0 {
		return fmt.Errorf("%w: unsupported opcode %d", errDNSQuery, hdr.OpCode)
	}
	if hdr.Truncated {
		return fmt.Errorf("%w: truncated flag set on query", errDNSQuery)
	}
	if qdCount := binary.BigEndian.Uint16(raw[4:6]); qdCount != 1 {
		return fmt.Errorf("%w: question count is %d, want 1", errDNSQuery, qdCount)
	}
	if anCount := binary.BigEndian.Uint16(raw[6:8]); anCount != 0 {
		return fmt.Errorf("%w: answer count is %d, want 0", errDNSQuery, anCount)
	}
	if nsCount := binary.BigEndian.Uint16(raw[8:10]); nsCount != 0 {
		return fmt.Errorf("%w: authority count is %d, want 0", errDNSQuery, nsCount)
	}
	if arCount := binary.BigEndian.Uint16(raw[10:12]); arCount > 1 {
		return fmt.Errorf("%w: additional count is %d, want at most 1", errDNSQuery, arCount)
	}
	return nil
}

func checkDNSQuerySections(p *dnsmessage.Parser, arCount uint16) error {
	if _, err := p.AnswerHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return fmt.Errorf("%w: unexpected answer section", errDNSQuery)
	}
	if _, err := p.AuthorityHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return fmt.Errorf("%w: unexpected authority section", errDNSQuery)
	}
	if arCount == 0 {
		if _, err := p.AdditionalHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
			return fmt.Errorf("%w: unexpected additional section", errDNSQuery)
		}
		return nil
	}
	ahdr, err := p.AdditionalHeader()
	if err != nil {
		return fmt.Errorf("parse DNS additional header: %w", err)
	}
	if ahdr.Type != dnsmessage.TypeOPT {
		return fmt.Errorf("%w: unsupported additional record type %v", errDNSQuery, ahdr.Type)
	}
	if ahdr.Name.String() != "." {
		return fmt.Errorf("%w: invalid OPT owner name %q", errDNSQuery, ahdr.Name.String())
	}
	if _, err := p.OPTResource(); err != nil {
		return fmt.Errorf("parse DNS OPT resource: %w", err)
	}
	if _, err := p.AdditionalHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return fmt.Errorf("%w: unexpected extra additional record", errDNSQuery)
	}
	return nil
}

func checkDNSQueryFraming(raw []byte, arCount uint16) error {
	off, err := skipDNSWireName(raw, dnsHeaderBytes)
	if err != nil {
		return err
	}
	if off+4 > len(raw) {
		return fmt.Errorf("%w: truncated question fields", errDNSQuery)
	}
	off += 4

	if arCount == 1 {
		off, err = skipDNSWireName(raw, off)
		if err != nil {
			return err
		}
		if off+10 > len(raw) {
			return fmt.Errorf("%w: truncated additional record header", errDNSQuery)
		}
		rdLen := int(binary.BigEndian.Uint16(raw[off+8 : off+10]))
		off += 10
		if off+rdLen > len(raw) {
			return fmt.Errorf("%w: truncated additional record data", errDNSQuery)
		}
		off += rdLen
	}

	if off != len(raw) {
		return fmt.Errorf("%w: %d trailing bytes beyond DNS message", errDNSQuery, len(raw)-off)
	}
	return nil
}

func skipDNSWireName(raw []byte, off int) (int, error) {
	pos := off
	limit := off
	nextOff := 0
	jumped := false
	totalLen := 0

	for {
		if pos >= len(raw) {
			return 0, fmt.Errorf("%w: truncated wire name", errDNSQuery)
		}
		b := raw[pos]
		if b == 0 {
			if !jumped {
				nextOff = pos + 1
			}
			return nextOff, nil
		}
		switch b & 0xc0 {
		case 0x00:
			var err error
			pos, totalLen, err = advanceDNSWireLabel(raw, pos, totalLen)
			if err != nil {
				return 0, err
			}
		case 0xc0:
			ptr, err := decodeDNSWirePointer(raw, pos, limit)
			if err != nil {
				return 0, err
			}
			if !jumped {
				nextOff = pos + 2
				jumped = true
			}
			pos = ptr
			limit = ptr
		default:
			return 0, fmt.Errorf("%w: unsupported label prefix 0x%02x", errDNSQuery, b&0xc0)
		}
	}
}

func advanceDNSWireLabel(raw []byte, pos, totalLen int) (nextPos, nextTotal int, err error) {
	labelLen := int(raw[pos])
	pos++
	if pos+labelLen > len(raw) {
		return 0, 0, fmt.Errorf("%w: truncated wire label", errDNSQuery)
	}
	if slices.Contains(raw[pos:pos+labelLen], '.') {
		return 0, 0, fmt.Errorf("%w: literal dot in wire label", errDNSQuery)
	}
	totalLen += 1 + labelLen
	if totalLen > maxDNSWireName {
		return 0, 0, fmt.Errorf("%w: wire name exceeds %d bytes", errDNSQuery, maxDNSWireName+1)
	}
	return pos + labelLen, totalLen, nil
}

func decodeDNSWirePointer(raw []byte, pos, limit int) (int, error) {
	if pos+2 > len(raw) {
		return 0, fmt.Errorf("%w: truncated compression pointer", errDNSQuery)
	}
	ptr := int(raw[pos]&0x3f)<<8 | int(raw[pos+1])
	if ptr < dnsHeaderBytes || ptr >= limit {
		return 0, fmt.Errorf("%w: invalid compression pointer", errDNSQuery)
	}
	return ptr, nil
}

// packDNSResponse builds a DNS response preserving q's ID, RecursionDesired
// flag and echoed question. On RCodeSuccess, A and AAAA queries emit only
// matching-family addresses from addrs; HTTPS, SVCB and all other types emit
// zero answers regardless of addrs. Any response over 65535 bytes is rejected.
func packDNSResponse(q dnsQuery, rcode dnsmessage.RCode, addrs []netip.Addr) ([]byte, error) {
	if q.question.Name.Length == 0 || q.question.Class != dnsmessage.ClassINET {
		return nil, fmt.Errorf("%w: invalid echoed question", errDNSResponse)
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 q.header.ID,
		Response:           true,
		OpCode:             0,
		RecursionDesired:   q.header.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	})
	b.EnableCompression()

	if err := b.StartQuestions(); err != nil {
		return nil, fmt.Errorf("start DNS response questions: %w", err)
	}
	if err := b.Question(q.question); err != nil {
		return nil, fmt.Errorf("pack DNS response question: %w", err)
	}

	if rcode == dnsmessage.RCodeSuccess {
		if err := packDNSAnswers(&b, q.question, addrs); err != nil {
			return nil, err
		}
	}

	pkt, err := b.Finish()
	if err != nil {
		return nil, fmt.Errorf("finish DNS response: %w", err)
	}
	if len(pkt) > maxDNSMessageBytes {
		return nil, fmt.Errorf("%w: response length %d exceeds %d-byte limit", errDNSResponse, len(pkt), maxDNSMessageBytes)
	}
	return pkt, nil
}

func isSuppressedSVCBType(t dnsmessage.Type) bool {
	return t == dnsmessage.TypeSVCB || t == dnsmessage.TypeHTTPS
}

func packDNSAnswers(b *dnsmessage.Builder, q dnsmessage.Question, addrs []netip.Addr) error {
	if isSuppressedSVCBType(q.Type) {
		return nil
	}
	switch q.Type {
	case dnsmessage.TypeA:
		return packDNSARecords(b, q.Name, addrs)
	case dnsmessage.TypeAAAA:
		return packDNSAAAARecords(b, q.Name, addrs)
	default:
		return nil
	}
}

func packDNSARecords(b *dnsmessage.Builder, name dnsmessage.Name, addrs []netip.Addr) error {
	if err := b.StartAnswers(); err != nil {
		return fmt.Errorf("start DNS A answers: %w", err)
	}
	rhdr := dnsmessage.ResourceHeader{
		Name:  name,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
		TTL:   dnsAnswerTTL,
	}
	emitted := 0
	for _, addr := range addrs {
		if !addr.Is4() || addr.Zone() != "" {
			continue
		}
		if emitted >= maxDNSAnswers {
			return fmt.Errorf("%w: response exceeds %d-byte limit", errDNSResponse, maxDNSMessageBytes)
		}
		if err := b.AResource(rhdr, dnsmessage.AResource{A: addr.As4()}); err != nil {
			return fmt.Errorf("pack DNS A answer: %w", err)
		}
		emitted++
	}
	return nil
}

func packDNSAAAARecords(b *dnsmessage.Builder, name dnsmessage.Name, addrs []netip.Addr) error {
	if err := b.StartAnswers(); err != nil {
		return fmt.Errorf("start DNS AAAA answers: %w", err)
	}
	rhdr := dnsmessage.ResourceHeader{
		Name:  name,
		Type:  dnsmessage.TypeAAAA,
		Class: dnsmessage.ClassINET,
		TTL:   dnsAnswerTTL,
	}
	emitted := 0
	for _, addr := range addrs {
		if !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
			continue
		}
		if emitted >= maxDNSAAAAAnswers {
			return fmt.Errorf("%w: response exceeds %d-byte limit", errDNSResponse, maxDNSMessageBytes)
		}
		if err := b.AAAAResource(rhdr, dnsmessage.AAAAResource{AAAA: addr.As16()}); err != nil {
			return fmt.Errorf("pack DNS AAAA answer: %w", err)
		}
		emitted++
	}
	return nil
}
