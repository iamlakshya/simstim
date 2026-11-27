// Package egress is the trusted per-session network enforcement process
// (<helper> egress <id>, spec 5.6 and 7.2). It receives streams from the
// session's supervisor over an inherited socket pair on fd 3, opens no
// listening sockets, checks policy, addresses and TLS ClientHello fields,
// and sends scrubbed net audit records back over its control stream.
package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const linkFD = 3

// Control operations and events between supervisor and egress.
const (
	CtlInit         = "init"
	CtlPolicyUpdate = "policy.update"
	EventAudit      = "audit"
	EventOnceEnd    = "once_end"
	EventRuleExpiry = "rule_expiry"
)

// InitArgs are the initial configuration and secret values sent once by the
// supervisor over the egress control stream before traffic is served.
type InitArgs struct {
	SessionID  string `json:"session_id"`
	SessionRef string `json:"session_ref"`
	Agent      string `json:"agent,omitempty"`
	// JudgeMetadata is trusted start data, source-checked before judge IO.
	JudgeMetadata classify.Metadata `json:"judge_metadata,omitempty"`
	// AgentKinds is the trusted saved-member seed. It requires member-add-v1.
	AgentKinds []string          `json:"agent_kinds,omitempty"`
	DNS        string            `json:"dns,omitempty"`
	Policy     api.Policy        `json:"policy"`
	Secrets    map[string]string `json:"secrets,omitempty"`
	// Decoys is private trusted-start tripwire input, never public state.
	Decoys []api.DecoyTripwire `json:"decoys,omitempty"`
}

// InitResult carries only public trust material out of the egress process.
// Signing keys and leaf keys have no control-link representation.
type InitResult struct {
	CACert string `json:"ca_cert"`
}

// PolicyUpdateArgs updates the live policy of a running egress process.
type PolicyUpdateArgs struct {
	// SourceLayers is the complete protected source vector for a live update.
	// A non-nil vector is recomputed against the actual applied policy.
	SourceLayers         []api.Layer `json:"source_layers,omitempty"`
	SessionRef           string      `json:"session_ref,omitempty"`
	Policy               api.Policy  `json:"policy"`
	ResetUnknownRefusals bool        `json:"reset_unknown_refusals,omitempty"`
}

// CtlEvent is an asynchronous event emitted by egress on its control stream.
type CtlEvent struct {
	ClassifierSession *api.NetworkHoldActionArgs `json:"classifier_session,omitempty"`
	Event             string                     `json:"event"`
	Audit             *AuditEntry                `json:"audit,omitempty"`
	OnceEnd           *OnceEndEvent              `json:"once_end,omitempty"`
	RuleExpiry        *RuleExpiryEvent           `json:"rule_expiry,omitempty"`
	Member            *MemberAudit               `json:"member,omitempty"`
	Minted            *MintedStateEvent          `json:"minted,omitempty"`
	NetworkHold       *NetworkHoldEvent          `json:"network_hold,omitempty"`
}

// OnceEndEvent identifies the exact one-use lease whose flow has ended.
type OnceEndEvent struct {
	ID      string    `json:"id"`
	GrantID string    `json:"grant_id,omitempty"`
	Until   time.Time `json:"until"`
}

// RuleExpiryEvent identifies a timed lease that has reached its deadline.
type RuleExpiryEvent OnceEndEvent

// StreamHeader is the JSON header of a link.KindEgress stream (spec 10.2).
type StreamHeader struct {
	Kind string `json:"kind,omitempty"`
	Via  string `json:"via"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// ParseStreamHeader strictly decodes and validates a KindEgress stream header.
func ParseStreamHeader(raw []byte) (StreamHeader, error) {
	if len(raw) == 0 || len(raw) > 4<<10 {
		return StreamHeader{}, errors.New("invalid egress stream header length")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var h StreamHeader
	if err := dec.Decode(&h); err != nil {
		return StreamHeader{}, fmt.Errorf("decode egress stream header: %w", err)
	}
	if dec.More() || dec.Decode(&struct{}{}) != io.EOF {
		return StreamHeader{}, errors.New("trailing data in egress stream header")
	}
	if h.Kind != "" && h.Kind != "egress" {
		return StreamHeader{}, fmt.Errorf("unexpected stream kind %q", h.Kind)
	}
	switch h.Via {
	case "explicit", "transparent":
	default:
		return StreamHeader{}, fmt.Errorf("invalid egress via %q", h.Via)
	}
	norm, err := NormaliseHost(h.Host)
	if err != nil {
		return StreamHeader{}, fmt.Errorf("invalid egress host: %w", err)
	}
	if h.Port < 1 || h.Port > 65535 {
		return StreamHeader{}, fmt.Errorf("invalid egress port %d", h.Port)
	}
	h.Host = norm
	return h, nil
}

// RefusalLine formats the one-line 403 refusal body and grant hint of spec 7.2.
func RefusalLine(sessionRef, host string, port int) string {
	ref := strings.TrimSpace(api.CleanText(sessionRef, 128))
	if ref == "" {
		ref = "session"
	}
	cleanHost := strings.TrimSpace(api.CleanText(host, 253))
	target := cleanHost
	if port > 0 && port != 443 {
		target = net.JoinHostPort(cleanHost, strconv.Itoa(port))
	}
	return fmt.Sprintf("%s: %s is not allowed in %s. Try: %s policy allow %s --in %s",
		brand.Name, target, ref, brand.Name, target, ref)
}

// LinkOptions returns the link.Options shared by the supervisor and the egress
// process on their private socket pair. The supervisor opens both ctl and
// egress streams.
func LinkOptions() link.Options {
	return link.Options{
		MaxSupervisorStreams: maxActiveFlows + maxActiveDNS + 8,
		MaxGuestStreams:      4,
		Opener: map[link.Kind]link.Side{
			link.KindCtl:    link.Supervisor,
			link.KindEgress: link.Supervisor,
			link.KindDNS:    link.Supervisor,
		},
	}
}

// Options configures a Server.
type Options struct {
	Popularity *PopularitySourceV1
	Dialer     *Dialer
	Logger     *slog.Logger
	Now        func() time.Time
	// OriginRoots overrides the system roots for an embedded server or test.
	// Nil loads the system roots once at process start.
	OriginRoots *x509.CertPool
}

// Server serves the egress end of a supervisor-to-egress link.
type Server struct {
	sess       *link.Session
	dialer     *Dialer
	log        *slog.Logger
	now        func() time.Time
	flows      *flowTracker
	dnsSem     chan struct{}
	scrubber   *Scrubber
	ledger     *onceLedger
	ca         *sessionAuthority
	transport  *http.Transport
	httpSem    chan struct{}
	originSem  chan struct{}
	expiryWake chan struct{}
	expired    map[onceKey]struct{}
	policyMu   registryGate
	holds      *networkHolds
	classifier *classifierRuntime
	usage      *ModelUsageMeterV1
	uploads    *uploadBudgets

	mu      sync.RWMutex
	engine  *Engine
	synth   *syntheticTable
	dnsMode string
	agent   string
	ref     string
	inited  bool
	members *memberState

	ctlMu     registryGate
	ctl       *proto.Writer
	ctlStream *link.Stream

	done      chan struct{}
	cancel    context.CancelFunc
	closeOnce sync.Once
}

// NewServer creates a Server over conn using opt. Before CtlInit completes,
// its engine denies every destination so early streams fail closed.
func NewServer(conn net.Conn, opt Options) (*Server, error) {
	dialer := opt.Dialer
	if dialer == nil {
		hostAddrs, err := HostInterfaceAddrs()
		if err != nil {
			return nil, fmt.Errorf("read host addresses: %w", err)
		}
		dialer = &Dialer{HostAddrs: hostAddrs}
	}
	logger := opt.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	nowFn := opt.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	failClosed, err := NewEngine("", api.Policy{Preset: api.PresetLocked})
	if err != nil {
		return nil, fmt.Errorf("build fail-closed engine: %w", err)
	}
	failClosed.now = nowFn
	ca, err := newSessionAuthority(nowFn())
	if err != nil {
		return nil, err
	}
	_, cancel := context.WithCancel(context.Background())
	srv := &Server{
		sess:       link.New(conn, link.Guest, LinkOptions()),
		dialer:     dialer,
		log:        logger,
		now:        nowFn,
		flows:      newFlowTracker(maxActiveFlows),
		dnsSem:     make(chan struct{}, maxActiveDNS),
		synth:      newSyntheticTable(MaxSyntheticNames),
		scrubber:   NewScrubber(nil),
		engine:     failClosed,
		ledger:     failClosed.ledger,
		ca:         ca,
		httpSem:    make(chan struct{}, maxActiveFlows),
		originSem:  make(chan struct{}, maxActiveFlows),
		expiryWake: make(chan struct{}, 1),
		expired:    make(map[onceKey]struct{}),
		done:       make(chan struct{}),
		cancel:     cancel,
	}
	srv.holds, err = newNetworkHolds(srv.scrubber)
	if err != nil {
		_ = srv.sess.Close()
		return nil, err
	}
	srv.transport, err = srv.newHTTPTransport(opt.OriginRoots)
	if err != nil {
		return nil, err
	}
	srv.classifier = newClassifierRuntime(srv)
	if err := srv.bindPopularitySourceV1(opt.Popularity); err != nil {
		_ = srv.Close()
		return nil, err
	}
	srv.usage = NewModelUsageMeterV1()
	srv.uploads = newUploadBudgets()
	return srv, nil
}

// Serve accepts control and egress streams until the link closes or Close runs.
func (s *Server) Serve() {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer s.Close()
	go s.watchExpiry(ctx)

	for {
		st, err := s.sess.Accept(ctx)
		if err != nil {
			return
		}
		switch st.Kind() {
		case link.KindCtl:
			go s.serveCtl(st)
		case link.KindEgress:
			go s.handleStream(ctx, st)
		case link.KindDNS:
			s.acceptDNSStream(ctx, st)
		default:
			_ = st.Close()
		}
	}
}

// Close stops all active flows and closes the underlying link.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.RLock()
		cancel := s.cancel
		s.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		if s.holds != nil {
			s.holds.close()
		}
		s.flows.closeAll()
		if s.uploads != nil {
			s.uploads.close()
		}
		if s.classifier != nil {
			s.classifier.close()
			s.classifier.facts.closePopularityV1()
		}
		s.dropMemoryGrants()
		if s.transport != nil {
			s.transport.CloseIdleConnections()
		}
		_ = s.sess.Close()
		s.joinSourceExport()
		close(s.done)
	})
	return nil
}

// Done is closed when the server has stopped.
func (s *Server) Done() <-chan struct{} { return s.done }

func (s *Server) handleStream(ctx context.Context, st *link.Stream) {
	if !s.flows.acquire(ctx) {
		_ = st.Close()
		return
	}
	defer s.flows.release()

	if !s.isInited() {
		_ = st.Close()
		return
	}
	var raw json.RawMessage
	if err := st.Header(&raw); err != nil {
		_ = st.Close()
		return
	}
	hdr, err := ParseStreamHeader(raw)
	if err != nil {
		_ = st.Close()
		return
	}
	var original struct {
		Host string `json:"host"`
	}
	if json.Unmarshal(raw, &original) != nil {
		_ = st.Close()
		return
	}
	ctx = context.WithValue(ctx, rawSecretDestinationKey{}, original.Host)
	s.ServeFlow(ctx, st, hdr)
}

func (s *Server) serveCtl(st *link.Stream) {
	w := proto.NewWriter(st)
	s.ctlMu.Lock()
	if s.ctl != nil {
		s.ctlMu.Unlock()
		_ = st.Close()
		return
	}
	s.ctl, s.ctlStream = w, st
	s.ctlMu.Unlock()

	r := bufio.NewReader(st)
	for {
		f, err := proto.Read(r)
		if err != nil {
			_ = s.Close()
			return
		}
		if f.Type != proto.TRequest {
			continue
		}
		var req proto.Request
		if json.Unmarshal(f.Payload, &req) != nil {
			continue
		}
		s.handleCtlRequest(w, req)
	}
}

func (s *Server) isInited() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inited
}

func (s *Server) currentEngine() *Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.engine
}

func (s *Server) agentName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agent
}

func (s *Server) sessionRef() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ref == "" {
		return "session"
	}
	return s.ref
}
