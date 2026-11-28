package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sync"

	"golang.org/x/net/dns/dnsmessage"
)

// DNSMode chooses whether allowed names receive real addresses or synthetic
// addresses from 198.18.0.0/15.
type DNSMode string

const (
	// DNSModeReal answers allowed names with their verified upstream IP addresses.
	DNSModeReal DNSMode = "real"
	// DNSModeSynthetic answers allowed (and optionally unknown) names with
	// synthetic IPv4 addresses from 198.18.0.0/15 without querying upstream.
	DNSModeSynthetic DNSMode = "synthetic"

	// RFC 9460 HTTPS (65) and SVCB (64) RR types.
	typeSVCB  dnsmessage.Type = 64
	typeHTTPS dnsmessage.Type = 65

	maxDNSMessageBytes = 4096
	defaultDNSTTL      = 60
)

// DNSOptions configures a DNSHandler.
type DNSOptions struct {
	Engine *Engine
	Addrs  *AddressPolicy
	Dialer *SafeDialer
	Mode   DNSMode
	Audit  AuditSink
}

// DNSHandler answers raw DNS wire messages according to the session policy
// using golang.org/x/net/dns/dnsmessage. Disallowed names receive NXDOMAIN
// without any upstream query; HTTPS and SVCB queries always receive empty
// NOERROR answers so clients do not learn ECH keys (spec 7.2).
type DNSHandler struct {
	engine *Engine
	addrs  *AddressPolicy
	dialer *SafeDialer
	mode   DNSMode
	audit  AuditSink

	mu        sync.Mutex
	nextSynth uint32 // offset within 198.18.0.0/15 (starts at 1: 198.18.0.1)
	nameToIP  map[string]netip.Addr
	ipToName  map[netip.Addr]string
}

// NewDNSHandler constructs a DNSHandler.
func NewDNSHandler(opts DNSOptions) *DNSHandler {
	mode := opts.Mode
	if mode == "" {
		mode = DNSModeReal
	}
	return &DNSHandler{
		engine:    opts.Engine,
		addrs:     opts.Addrs,
		dialer:    opts.Dialer,
		mode:      mode,
		audit:     opts.Audit,
		nextSynth: 1,
		nameToIP:  map[string]netip.Addr{},
		ipToName:  map[netip.Addr]string{},
	}
}

// LookupSynthetic returns the original hostname assigned to a synthetic
// 198.18.0.0/15 address in synthetic DNS mode.
func (h *DNSHandler) LookupSynthetic(addr netip.Addr) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	name, ok := h.ipToName[addr.WithZone("").Unmap()]
	return name, ok
}

// Handle processes one raw DNS wire query and returns the wire response.
func (h *DNSHandler) Handle(ctx context.Context, raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxDNSMessageBytes {
		return nil, errors.New("egress: DNS query size out of bounds. Try: send a valid DNS query under 4096 bytes")
	}
	var p dnsmessage.Parser
	hdr, err := p.Start(raw)
	if err != nil {
		return nil, fmt.Errorf("egress: parse DNS header: %w. Try: send a valid DNS query", err)
	}
	if hdr.Response {
		return nil, errors.New("egress: DNS message is a response, not a query. Try: send a DNS query")
	}
	q, err := p.Question()
	if err != nil {
		return buildEmptyDNS(hdr, dnsmessage.Question{}, dnsmessage.RCodeFormatError)
	}
	// Refuse multi-question queries cleanly.
	if _, err := p.Question(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeFormatError)
	}

	name, err := normalizeHost(q.Name.String())
	if err != nil {
		h.emitAudit(q.Name.String(), "invalid", string(ActionDeny), err.Error())
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeNameError)
	}

	// Spec 7.2: HTTPS and SVCB queries always get empty NOERROR answers without
	// reaching upstream so clients cannot discover ECH keys.
	if q.Type == typeHTTPS || q.Type == typeSVCB {
		h.emitAudit(name, "ech-suppress", string(ActionAllow), "empty answer for HTTPS/SVCB")
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeSuccess)
	}

	dec := h.engine.DecideDNS(name)
	if dec.Action != ActionAllow {
		h.emitAudit(name, dec.Rule, string(ActionDeny), dec.Why)
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeNameError)
	}

	if h.mode == DNSModeSynthetic {
		return h.answerSynthetic(hdr, q, name, dec)
	}
	return h.answerReal(ctx, hdr, q, name, dec)
}

func (h *DNSHandler) answerSynthetic(hdr dnsmessage.Header, q dnsmessage.Question, name string, dec Decision) ([]byte, error) {
	ip, err := h.allocSynthetic(name)
	if err != nil {
		h.emitAudit(name, dec.Rule, "error", err.Error())
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeServerFailure)
	}
	h.emitAudit(name, dec.Rule, auditDecision(dec), "synthetic")
	if q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeALL {
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeSuccess)
	}
	return buildAddrDNS(hdr, q, []netip.Addr{ip})
}

func (h *DNSHandler) allocSynthetic(name string) (netip.Addr, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ip, ok := h.nameToIP[name]; ok {
		return ip, nil
	}
	// 198.18.0.0/15 holds 2^17 = 131072 addresses (0x00001..0x1fffe).
	if h.nextSynth >= (1<<17)-1 {
		return netip.Addr{}, errors.New("egress: synthetic DNS pool 198.18.0.0/15 exhausted. Try: start a fresh session")
	}
	n := h.nextSynth
	h.nextSynth++
	b := [4]byte{198, byte(18 + (n >> 16)), byte((n >> 8) & 0xff), byte(n & 0xff)}
	ip := netip.AddrFrom4(b)
	h.nameToIP[name] = ip
	h.ipToName[ip] = name
	return ip, nil
}

func (h *DNSHandler) answerReal(ctx context.Context, hdr dnsmessage.Header, q dnsmessage.Question, name string, dec Decision) ([]byte, error) {
	// Only resolve A and AAAA records so DNS tunnelling over TXT/MX/SRV cannot
	// leave the host even on allowed names.
	if q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA {
		h.emitAudit(name, dec.Rule, auditDecision(dec), "unsupported RR type suppressed")
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeSuccess)
	}
	addrs, err := h.dialer.ResolveAndCheck(ctx, name, 443)
	if err != nil {
		var refused *RefusedAddrError
		if errors.As(err, &refused) {
			h.emitAudit(name, "range", string(ActionDeny), err.Error())
			return buildEmptyDNS(hdr, q, dnsmessage.RCodeNameError)
		}
		h.emitAudit(name, dec.Rule, "error", err.Error())
		return buildEmptyDNS(hdr, q, dnsmessage.RCodeServerFailure)
	}
	filtered := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if q.Type == dnsmessage.TypeA && a.Is4() {
			filtered = append(filtered, a)
		} else if q.Type == dnsmessage.TypeAAAA && a.Is6() {
			filtered = append(filtered, a)
		}
	}
	h.emitAudit(name, dec.Rule, auditDecision(dec), "")
	return buildAddrDNS(hdr, q, filtered)
}

func (h *DNSHandler) emitAudit(host, rule, decision, why string) {
	if h.audit == nil {
		return
	}
	h.audit(AuditEvent{
		V: 1, Kind: "dns", Host: host, Mode: string(h.mode),
		Rule: rule, Decision: decision, Why: why,
	})
}

// ServeStream handles one length-prefixed DNS query over a link stream
// (2-byte big-endian length prefix, RFC 1035 TCP DNS framing) and writes back
// the length-prefixed response.
func (h *DNSHandler) ServeStream(ctx context.Context, rw io.ReadWriteCloser) {
	defer func() { _ = rw.Close() }()
	var lenBuf [2]byte
	if _, err := io.ReadFull(rw, lenBuf[:]); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	if n == 0 || n > maxDNSMessageBytes {
		return
	}
	query := make([]byte, n)
	if _, err := io.ReadFull(rw, query); err != nil {
		return
	}
	resp, err := h.Handle(ctx, query)
	if err != nil || len(resp) == 0 || len(resp) > maxDNSMessageBytes {
		return
	}
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(resp))) //nolint:gosec // G115: len(resp) <= maxDNSMessageBytes (4096)
	_, _ = rw.Write(lenBuf[:])
	_, _ = rw.Write(resp)
}

func buildEmptyDNS(reqHdr dnsmessage.Header, q dnsmessage.Question, rcode dnsmessage.RCode) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 reqHdr.ID,
		Response:           true,
		RecursionDesired:   reqHdr.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	})
	if q.Name.Length > 0 {
		if err := b.StartQuestions(); err != nil {
			return nil, fmt.Errorf("egress: start DNS questions: %w", err)
		}
		if err := b.Question(q); err != nil {
			return nil, fmt.Errorf("egress: write DNS question: %w", err)
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil, fmt.Errorf("egress: finish DNS message: %w", err)
	}
	return out, nil
}

func buildAddrDNS(reqHdr dnsmessage.Header, q dnsmessage.Question, addrs []netip.Addr) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 reqHdr.ID,
		Response:           true,
		RecursionDesired:   reqHdr.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	})
	if err := b.StartQuestions(); err != nil {
		return nil, fmt.Errorf("egress: start DNS questions: %w", err)
	}
	if err := b.Question(q); err != nil {
		return nil, fmt.Errorf("egress: write DNS question: %w", err)
	}
	if len(addrs) > 0 {
		if err := b.StartAnswers(); err != nil {
			return nil, fmt.Errorf("egress: start DNS answers: %w", err)
		}
		for _, a := range addrs {
			if a.Is4() {
				if err := b.AResource(
					dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: defaultDNSTTL},
					dnsmessage.AResource{A: a.As4()},
				); err != nil {
					return nil, fmt.Errorf("egress: write A answer: %w", err)
				}
			} else if a.Is6() {
				if err := b.AAAAResource(
					dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: defaultDNSTTL},
					dnsmessage.AAAAResource{AAAA: a.As16()},
				); err != nil {
					return nil, fmt.Errorf("egress: write AAAA answer: %w", err)
				}
			}
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil, fmt.Errorf("egress: finish DNS response: %w", err)
	}
	return out, nil
}
