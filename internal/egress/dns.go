package egress

import (
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
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// DNS policy modes served by egress on KindDNS streams.
const (
	DNSModeReal      = "real"
	DNSModeSynthetic = "synthetic"

	// MaxDNSMessageBytes is the maximum raw DNS packet length carried over a
	// KindDNS stream.
	MaxDNSMessageBytes = maxDNSMessageBytes

	// MaxActiveDNS caps concurrent DNS queries served by one egress process,
	// separate from active TCP flows.
	MaxActiveDNS = maxActiveDNS

	maxActiveDNS          = 32
	maxRealDNSLookupAddrs = maxDNSAnswers + maxDNSAAAAAnswers
	dnsStreamTimeout      = 5 * time.Second
	maxDNSStreamHeader    = 256
)

type dnsStreamHeader struct {
	Kind string `json:"kind,omitempty"`
}

// ParseDNSMode validates a session DNS mode: empty (disabled), real or
// synthetic.
func ParseDNSMode(s string) (string, error) {
	mode := strings.TrimSpace(s)
	switch mode {
	case "", DNSModeReal, DNSModeSynthetic:
		return mode, nil
	default:
		return "", &proto.Error{
			Code:    "bad_request",
			Message: fmt.Sprintf("dns mode %q must be empty, real or synthetic", s),
			Hint:    "Update the host helper, then resume the session.",
		}
	}
}

// ParseDNSStreamHeader strictly validates the JSON header of a KindDNS stream.
// Only {} or {"kind":"dns"} is allowed so a guest stream cannot override mode
// or pass extra fields.
func ParseDNSStreamHeader(raw []byte) error {
	if len(raw) == 0 || len(raw) > maxDNSStreamHeader {
		return errors.New("invalid DNS stream header length")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("DNS stream header must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var hdr dnsStreamHeader
	if err := dec.Decode(&hdr); err != nil {
		return fmt.Errorf("decode DNS stream header: %w", err)
	}
	if dec.More() || dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing data in DNS stream header")
	}
	if hdr.Kind != "" && hdr.Kind != string(link.KindDNS) {
		return fmt.Errorf("unexpected DNS stream kind %q", hdr.Kind)
	}
	return nil
}

func (s *Server) acceptDNSStream(ctx context.Context, st *link.Stream) {
	select {
	case s.dnsSem <- struct{}{}:
		go func(dnsSt *link.Stream) {
			defer func() { <-s.dnsSem }()
			s.handleDNSStream(ctx, dnsSt)
		}(st)
	default:
		_ = st.Close()
	}
}

func (s *Server) handleDNSStream(ctx context.Context, st *link.Stream) {
	defer st.Close()
	if ctx.Err() != nil {
		return
	}
	inited, mode, _, _ := s.dnsSnapshot()
	if !inited || mode == "" {
		return
	}
	var rawHdr json.RawMessage
	if err := st.Header(&rawHdr); err != nil {
		return
	}
	if err := ParseDNSStreamHeader(rawHdr); err != nil {
		return
	}

	qctx, cancel := context.WithTimeout(ctx, dnsStreamTimeout)
	defer cancel()
	stop := context.AfterFunc(qctx, func() { _ = st.Close() })
	defer stop()
	_ = st.SetDeadline(time.Now().Add(dnsStreamTimeout))

	reqBytes, err := io.ReadAll(io.LimitReader(st, maxDNSMessageBytes+1))
	if err != nil || len(reqBytes) < dnsHeaderBytes || len(reqBytes) > maxDNSMessageBytes {
		return
	}
	respBytes := s.AnswerDNS(qctx, reqBytes)
	if len(respBytes) == 0 || qctx.Err() != nil {
		return
	}
	_, _ = st.Write(respBytes)
	_ = st.CloseWrite()
}

func (s *Server) dnsSnapshot() (inited bool, mode string, eng *Engine, synth *syntheticTable) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inited, s.dnsMode, s.engine, s.synth
}

// AnswerDNS evaluates one raw DNS packet under the server's initialised DNS
// mode and live policy, returning the wire response or nil when the packet
// must be dropped without a reply.
func (s *Server) AnswerDNS(ctx context.Context, raw []byte) []byte {
	inited, mode, eng, synth := s.dnsSnapshot()
	if !inited || (mode != DNSModeReal && mode != DNSModeSynthetic) || eng == nil {
		return nil
	}

	q, err := parseDNSQuery(raw)
	if err != nil {
		if q.question.Name.Length != 0 {
			if refusal := s.checkSecretDestination(ctx, q.question.Name.String()); refusal != nil {
				packet, _ := packDNSResponse(q, dnsmessage.RCodeRefused, nil)
				s.emitAudit(s.formatAudit(AuditEntry{Host: decoyAuditDestination(q.question.Name.String(), refusal), Mode: "dns", Method: "DNS", Status: http.StatusForbidden, Rule: refusal.auditRule(), Secret: refusal.decoy, Decision: "deny"}))
				return packet
			}
		}
		return classifyDNSQueryError(raw)
	}

	if refusal := s.checkSecretDestination(ctx, q.question.Name.String()); refusal != nil {
		packet, _ := packDNSResponse(q, dnsmessage.RCodeRefused, nil)
		s.emitAudit(s.formatAudit(AuditEntry{Host: decoyAuditDestination(q.host, refusal), Mode: "dns", Method: "DNS", Status: http.StatusForbidden, Rule: refusal.auditRule(), Secret: refusal.decoy, Decision: "deny"}))
		return packet
	}
	if s.decoyHost(q.host, 443) {
		return answerSyntheticDNS(synth, q, dnsHostVerdict{allowedPorts: []int{443}})
	}
	eng = s.currentEngine()
	verdict := eng.evaluateDNS(q.host, s.now())
	switch mode {
	case DNSModeReal:
		return s.answerRealDNS(ctx, eng, q, verdict, raw)
	case DNSModeSynthetic:
		return answerSyntheticDNS(synth, q, verdict)
	default:
		return nil
	}
}

func (s *Server) answerRealDNS(ctx context.Context, eng *Engine, q dnsQuery, verdict dnsHostVerdict, raw []byte) []byte {
	if !verdict.permitsReal() {
		pkt, _ := packDNSResponse(q, dnsmessage.RCodeNameError, nil)
		return pkt
	}
	switch q.question.Type {
	case dnsmessage.TypeA, dnsmessage.TypeAAAA:
		addrs, rcode := s.resolveRealDNS(ctx, eng, q.host, verdict.allowedPorts)
		pkt, err := packDNSResponse(q, rcode, addrs)
		if err != nil {
			return packEchoedQuestionDNSReply(q.header, q.question, dnsmessage.RCodeServerFailure, raw)
		}
		return pkt
	default:
		pkt, _ := packDNSResponse(q, dnsmessage.RCodeSuccess, nil)
		return pkt
	}
}

func answerSyntheticDNS(synth *syntheticTable, q dnsQuery, verdict dnsHostVerdict) []byte {
	if !verdict.permitsSynthetic() {
		pkt, _ := packDNSResponse(q, dnsmessage.RCodeNameError, nil)
		return pkt
	}
	if q.question.Type == dnsmessage.TypeA {
		if synth == nil {
			pkt, _ := packDNSResponse(q, dnsmessage.RCodeServerFailure, nil)
			return pkt
		}
		addr, ok := synth.assign(q.host)
		if !ok {
			pkt, _ := packDNSResponse(q, dnsmessage.RCodeServerFailure, nil)
			return pkt
		}
		pkt, _ := packDNSResponse(q, dnsmessage.RCodeSuccess, []netip.Addr{addr})
		return pkt
	}
	pkt, _ := packDNSResponse(q, dnsmessage.RCodeSuccess, nil)
	return pkt
}

func (s *Server) resolveRealDNS(ctx context.Context, eng *Engine, host string, allowedPorts []int) ([]netip.Addr, dnsmessage.RCode) {
	timeout := s.dialer.Timeout
	if timeout <= 0 {
		timeout = dnsStreamTimeout
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lookup := s.dialer.Lookup
	if lookup == nil {
		resolver := s.dialer.resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		lookup = func(c context.Context, h string) ([]netip.Addr, error) {
			return resolver.LookupNetIP(c, "ip", strings.TrimSuffix(h, ".")+".")
		}
	}
	rawAddrs, err := lookup(lctx, host)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, dnsmessage.RCodeNameError
		}
		return nil, dnsmessage.RCodeServerFailure
	}
	if len(rawAddrs) > maxRealDNSLookupAddrs {
		return nil, dnsmessage.RCodeServerFailure
	}
	verified := make([]netip.Addr, 0, len(rawAddrs))
	precheck := func(addr netip.Addr, port int) bool { return eng.precheckResolvedAddr(host, addr, port) }
	for _, rawAddr := range rawAddrs {
		if !rawAddr.IsValid() {
			return nil, dnsmessage.RCodeRefused
		}
		ip := rawAddr.WithZone("")
		if !s.addrAllowedOnAnyPort(ip, allowedPorts, precheck) {
			return nil, dnsmessage.RCodeRefused
		}
		verified = append(verified, ip.Unmap())
	}
	return verified, dnsmessage.RCodeSuccess
}

func (s *Server) addrAllowedOnAnyPort(ip netip.Addr, ports []int, allow AllowAddrFunc) bool {
	for _, port := range ports {
		if _, ok := s.dialer.CheckAddr(ip, port, allow); ok {
			return true
		}
	}
	return false
}

func classifyDNSQueryError(raw []byte) []byte {
	if len(raw) < dnsHeaderBytes || len(raw) > maxDNSMessageBytes {
		return nil
	}
	if raw[2]&0x80 != 0 {
		return nil
	}
	var p dnsmessage.Parser
	hdr, err := p.Start(raw)
	if err != nil {
		return packHeaderOnlyDNSReply(raw, dnsmessage.RCodeFormatError)
	}
	if hdr.OpCode != 0 {
		return packHeaderOnlyDNSReply(raw, dnsmessage.RCodeNotImplemented)
	}
	if err := checkDNSQueryHeader(hdr, raw); err != nil {
		return packHeaderOnlyDNSReply(raw, dnsmessage.RCodeFormatError)
	}
	q, err := p.Question()
	if err != nil {
		return packHeaderOnlyDNSReply(raw, dnsmessage.RCodeFormatError)
	}
	if _, err := p.Question(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return packHeaderOnlyDNSReply(raw, dnsmessage.RCodeFormatError)
	}
	arCount := binary.BigEndian.Uint16(raw[10:12])
	if err := checkDNSQuerySections(&p, arCount); err != nil {
		return packHeaderOnlyDNSReply(raw, dnsmessage.RCodeFormatError)
	}
	if err := checkDNSQueryFraming(raw, arCount); err != nil {
		return packHeaderOnlyDNSReply(raw, dnsmessage.RCodeFormatError)
	}
	if q.Class != dnsmessage.ClassINET {
		return packEchoedQuestionDNSReply(hdr, q, dnsmessage.RCodeRefused, raw)
	}
	return packEchoedQuestionDNSReply(hdr, q, dnsmessage.RCodeNameError, raw)
}

func packHeaderOnlyDNSReply(raw []byte, rcode dnsmessage.RCode) []byte {
	var out [dnsHeaderBytes]byte
	out[0], out[1] = raw[0], raw[1]
	out[2] = 0x80 | (raw[2] & 0x79)
	out[3] = 0x80 | byte(rcode&0x0f)
	return out[:]
}

func packEchoedQuestionDNSReply(hdr dnsmessage.Header, q dnsmessage.Question, rcode dnsmessage.RCode, raw []byte) []byte {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 hdr.ID,
		Response:           true,
		OpCode:             0,
		RecursionDesired:   hdr.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	})
	if err := b.StartQuestions(); err != nil {
		return packHeaderOnlyDNSReply(raw, rcode)
	}
	if err := b.Question(q); err != nil {
		return packHeaderOnlyDNSReply(raw, rcode)
	}
	pkt, err := b.Finish()
	if err != nil || len(pkt) > maxDNSMessageBytes {
		return packHeaderOnlyDNSReply(raw, rcode)
	}
	return pkt
}
