package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"time"

	"github.com/elazarl/goproxy"
)

// ServerOptions configures an egress Server.
type ServerOptions struct {
	Engine          *Engine
	Addrs           *AddressPolicy
	Dialer          *SafeDialer
	Redactor        *Redactor
	Secrets         *SecretStore
	Usage           *UsageTracker
	UploadBudget    *UploadBudget
	CA              *SessionCA  // optional override; nil generates a fresh per-session CA in memory
	UpstreamTLS     *tls.Config // optional test override; production uses verified system roots
	GatewayRoutes   []GatewayRouteConfig
	GatewayTLS      *tls.Config // optional test override; production uses verified system roots
	Audit           AuditSink
	DNSMode         DNSMode
	SNITimeout      time.Duration
	NonproxyHandler http.Handler // optional override for relative-path requests; nil mounts Gateway
}

// Server is the egress HTTP CONNECT proxy, plain HTTP forward proxy, model
// gateway, transparent flow handler and DNS policy responder for one walled
// session.
type Server struct {
	Engine       *Engine
	Addrs        *AddressPolicy
	Dialer       *SafeDialer
	Redactor     *Redactor
	Secrets      *SecretStore
	Usage        *UsageTracker
	UploadBudget *UploadBudget
	CA           *SessionCA
	Gateway      *Gateway
	DNS          *DNSHandler

	audit          AuditSink
	sniTimeout     time.Duration
	proxy          *goproxy.ProxyHttpServer
	interceptProxy *httputil.ReverseProxy
}

type noopLogger struct{}

func (noopLogger) Printf(string, ...any) {}

// NewServer builds a Server backed by elazarl/goproxy, crypto/tls SNI checks,
// safe single-lookup dialing and DNS policy enforcement.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Engine == nil {
		return nil, errors.New("egress: ServerOptions.Engine is required. Try: construct an Engine with NewEngine")
	}
	addrs := opts.Addrs
	if addrs == nil {
		var err error
		addrs, err = NewAddressPolicy()
		if err != nil {
			return nil, err
		}
	}
	dialer := opts.Dialer
	if dialer == nil {
		dialer = NewSafeDialer(addrs, opts.Engine)
	}
	redactor := opts.Redactor
	if redactor == nil {
		redactor = NewRedactor(nil)
	}
	secrets := opts.Secrets
	if secrets == nil {
		secrets = NewSecretStore()
	}
	usage := opts.Usage
	if usage == nil {
		usage = NewUsageTracker()
	}
	uploadBudget := opts.UploadBudget
	if uploadBudget == nil {
		uploadBudget = NewUploadBudget(opts.Engine.UploadBudget())
	}
	ca := opts.CA
	if ca == nil {
		var err error
		ca, err = NewSessionCA()
		if err != nil {
			return nil, err
		}
	}
	sniTimeout := opts.SNITimeout
	if sniTimeout <= 0 {
		sniTimeout = defaultSNITimeout
	}

	s := &Server{
		Engine:       opts.Engine,
		Addrs:        addrs,
		Dialer:       dialer,
		Redactor:     redactor,
		Secrets:      secrets,
		Usage:        usage,
		UploadBudget: uploadBudget,
		CA:           ca,
		audit:        opts.Audit,
		sniTimeout:   sniTimeout,
	}
	s.DNS = NewDNSHandler(DNSOptions{
		Engine: opts.Engine,
		Addrs:  addrs,
		Dialer: dialer,
		Mode:   opts.DNSMode,
		Audit:  opts.Audit,
	})
	nonproxy := opts.NonproxyHandler
	if nonproxy == nil {
		routes := opts.GatewayRoutes
		if len(routes) == 0 {
			routes = DefaultGatewayRoutes(opts.Engine.snap.Load().agent)
		}
		gw, err := NewGateway(GatewayOptions{
			Engine:    opts.Engine,
			Dialer:    dialer,
			Secrets:   secrets,
			Usage:     usage,
			Redactor:  redactor,
			Audit:     opts.Audit,
			Routes:    routes,
			TLSConfig: opts.GatewayTLS,
		})
		if err != nil {
			return nil, err
		}
		s.Gateway = gw
		nonproxy = gw
	}
	s.proxy = s.buildProxy(nonproxy, opts.UpstreamTLS)
	s.interceptProxy = s.buildInterceptProxy(s.proxy.Tr)
	return s, nil
}

// Close wipes any in-memory bound credentials and per-session CA keys held by
// the server.
func (s *Server) Close() {
	if s == nil {
		return
	}
	if s.Secrets != nil {
		s.Secrets.Close()
	}
	if s.CA != nil {
		s.CA.Close()
	}
}

func (s *Server) buildProxy(nonproxy http.Handler, upstreamTLS *tls.Config) *goproxy.ProxyHttpServer {
	p := goproxy.NewProxyHttpServer()
	p.Logger = noopLogger{}
	p.AllowHTTP2 = true
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if upstreamTLS != nil {
		tlsCfg = upstreamTLS.Clone()
		if tlsCfg.MinVersion < tls.VersionTLS12 {
			tlsCfg.MinVersion = tls.VersionTLS12
		}
		tlsCfg.InsecureSkipVerify = false
	}
	// Replace goproxy's default Transport (which skips TLS verification and
	// reads host proxy env vars) with our safe single-lookup Transport.
	p.Tr = &http.Transport{
		DialContext:           s.Dialer.DialContext,
		Proxy:                 nil,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if nonproxy != nil {
		p.NonproxyHandler = nonproxy
	} else {
		p.NonproxyHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "egress proxy: direct non-proxy requests are not allowed", http.StatusForbidden)
		})
	}
	p.OnRequest().HandleConnectFunc(s.handleConnect)
	p.OnRequest().DoFunc(s.handleHTTPRequest)
	p.OnResponse().DoFunc(s.handleHTTPResponse)
	return p
}

// ServeHTTP delegates explicit HTTP proxy and CONNECT requests to goproxy.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.proxy.ServeHTTP(w, r)
}

func (s *Server) emitAudit(ev AuditEvent) {
	if s.audit == nil {
		return
	}
	ev.V = 1
	if ev.T.IsZero() {
		ev.T = time.Now().UTC()
	}
	s.audit(ev)
}

func (s *Server) denyBody(host string, port int, dec Decision) string {
	if dec.Why != "" && len(dec.Rule) >= len("gateway:") && dec.Rule[:len("gateway:")] == "gateway:" {
		return dec.Why
	}
	return s.Engine.DenyHint(host, port)
}

func auditDecision(d Decision) string {
	if d.Action != ActionAllow {
		return string(ActionDeny)
	}
	if d.Rule == "unknown:allow" {
		return "allow-unknown"
	}
	return string(ActionAllow)
}

func (s *Server) handleConnect(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
	normHost, port, err := parseConnectAuthority(host)
	if err != nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: host, Mode: string(ModeSplice), Method: http.MethodConnect,
			Status: http.StatusBadRequest, Rule: "invalid", Decision: string(ActionDeny), Why: err.Error(),
		})
		ctx.Resp = textResponse(ctx.Req, http.StatusBadRequest, err.Error()+"\n") //nolint:bodyclose // goproxy writes and closes ctx.Resp.Body
		return goproxy.RejectConnect, host
	}

	dec := s.Engine.Decide(Request{Host: normHost, Port: port, Method: http.MethodConnect})
	if dec.Action != ActionAllow {
		body := s.denyBody(normHost, port, dec)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
			Status: http.StatusForbidden, Rule: dec.Rule, Decision: string(ActionDeny), Why: dec.Why,
		})
		ctx.Resp = textResponse(ctx.Req, http.StatusForbidden, body+"\n") //nolint:bodyclose // goproxy writes and closes ctx.Resp.Body
		return goproxy.RejectConnect, host
	}

	if used, limit, ok := s.checkUploadBudget(normHost); !ok {
		body := uploadBudgetMessage(s.Engine.Session(), normHost, used, limit)
		s.auditUploadBudget(normHost, port, string(ModeSplice), http.MethodConnect, "", 0, 0, used, limit)
		ctx.Resp = textResponse(ctx.Req, http.StatusForbidden, body+"\n") //nolint:bodyclose // goproxy writes and closes ctx.Resp.Body
		return goproxy.RejectConnect, host
	}

	addrs, err := s.Dialer.ResolveAndCheck(ctx.Req.Context(), normHost, port)
	if err != nil {
		status, decision := errHTTPStatus(err)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
			Status: status, Rule: "range", Decision: decision, Why: err.Error(),
		})
		ctx.Resp = textResponse(ctx.Req, status, err.Error()+"\n") //nolint:bodyclose // goproxy writes and closes ctx.Resp.Body
		return goproxy.RejectConnect, host
	}

	if dec.Mode == ModeIntercept {
		return &goproxy.ConnectAction{
			Action: goproxy.ConnectHijack,
			Hijack: func(req *http.Request, clientConn net.Conn, _ *goproxy.ProxyCtx) {
				s.hijackInterceptConnect(req.Context(), clientConn, normHost, port, addrs)
			},
		}, host
	}

	return &goproxy.ConnectAction{
		Action: goproxy.ConnectHijack,
		Hijack: func(req *http.Request, clientConn net.Conn, _ *goproxy.ProxyCtx) {
			s.hijackConnect(req.Context(), clientConn, normHost, port, dec, addrs)
		},
	}, host
}

func (s *Server) hijackConnect(ctx context.Context, clientConn net.Conn, host string, port int, dec Decision, addrs []netip.Addr) {
	defer func() { _ = clientConn.Close() }()

	if port == 443 {
		if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
		ch, replay, err := PeekClientHello(ctx, clientConn, s.sniTimeout)
		if err == nil {
			err = CheckClientHello(ch, host)
		}
		if err != nil {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
				Status: http.StatusForbidden, Rule: "sni", Decision: string(ActionDeny), Why: err.Error(),
			})
			return
		}
		upstream, _, err := s.Dialer.DialChecked(ctx, addrs, port)
		if err != nil {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
				Status: http.StatusBadGateway, Rule: dec.Rule, Decision: "error", Why: err.Error(),
			})
			return
		}
		defer func() { _ = upstream.Close() }()
		if _, exceeded, used, limit := s.UploadBudget.Consume(host, int64(len(replay))); exceeded {
			s.auditUploadBudget(host, port, string(ModeSplice), http.MethodConnect, "", 0, 0, used, limit)
			return
		}
		if _, err := upstream.Write(replay); err != nil {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
				Status: http.StatusBadGateway, Rule: dec.Rule, Decision: "error", Why: err.Error(),
			})
			return
		}
		up, down, budgetHit := spliceConnsWithBudget(ctx, clientConn, upstream, host, s.UploadBudget)
		totalUp := int64(len(replay)) + up
		if budgetHit {
			_, used, limit, _ := s.UploadBudget.Remaining(host)
			s.auditUploadBudget(host, port, string(ModeSplice), http.MethodConnect, "", totalUp, down, used, limit)
			return
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
			Status: http.StatusOK, Up: totalUp, Down: down,
			Rule: dec.Rule, Decision: auditDecision(dec),
		})
		return
	}

	upstream, _, err := s.Dialer.DialChecked(ctx, addrs, port)
	if err != nil {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n"+err.Error()+"\n")
		s.emitAudit(AuditEvent{
			Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
			Status: http.StatusBadGateway, Rule: dec.Rule, Decision: "error", Why: err.Error(),
		})
		return
	}
	defer func() { _ = upstream.Close() }()
	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	up, down, budgetHit := spliceConnsWithBudget(ctx, clientConn, upstream, host, s.UploadBudget)
	if budgetHit {
		_, used, limit, _ := s.UploadBudget.Remaining(host)
		s.auditUploadBudget(host, port, string(ModeSplice), http.MethodConnect, "", up, down, used, limit)
		return
	}
	s.emitAudit(AuditEvent{
		Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
		Status: http.StatusOK, Up: up, Down: down,
		Rule: dec.Rule, Decision: auditDecision(dec),
	})
}
