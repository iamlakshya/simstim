package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	Audit           AuditSink
	DNSMode         DNSMode
	SNITimeout      time.Duration
	NonproxyHandler http.Handler // optional handler for relative-path requests (e.g. model gateway)
}

// Server is the egress HTTP CONNECT proxy, plain HTTP forward proxy,
// transparent flow handler and DNS policy responder for one walled session.
type Server struct {
	Engine   *Engine
	Addrs    *AddressPolicy
	Dialer   *SafeDialer
	Redactor *Redactor
	DNS      *DNSHandler

	audit      AuditSink
	sniTimeout time.Duration
	proxy      *goproxy.ProxyHttpServer
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
	sniTimeout := opts.SNITimeout
	if sniTimeout <= 0 {
		sniTimeout = defaultSNITimeout
	}

	s := &Server{
		Engine:     opts.Engine,
		Addrs:      addrs,
		Dialer:     dialer,
		Redactor:   redactor,
		audit:      opts.Audit,
		sniTimeout: sniTimeout,
	}
	s.DNS = NewDNSHandler(DNSOptions{
		Engine: opts.Engine,
		Addrs:  addrs,
		Dialer: dialer,
		Mode:   opts.DNSMode,
		Audit:  opts.Audit,
	})
	s.proxy = s.buildProxy(opts.NonproxyHandler)
	return s, nil
}

func (s *Server) buildProxy(nonproxy http.Handler) *goproxy.ProxyHttpServer {
	p := goproxy.NewProxyHttpServer()
	p.Logger = noopLogger{}
	// Replace goproxy's default Transport (which skips TLS verification and
	// reads host proxy env vars) with our safe single-lookup Transport.
	p.Tr = &http.Transport{
		DialContext:           s.Dialer.DialContext,
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
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
		body := s.Engine.DenyHint(normHost, port)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
			Status: http.StatusForbidden, Rule: dec.Rule, Decision: string(ActionDeny), Why: dec.Why,
		})
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
		if _, err := upstream.Write(replay); err != nil {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
				Status: http.StatusBadGateway, Rule: dec.Rule, Decision: "error", Why: err.Error(),
			})
			return
		}
		up, down := spliceConns(ctx, clientConn, upstream)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
			Status: http.StatusOK, Up: int64(len(replay)) + up, Down: down,
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
	up, down := spliceConns(ctx, clientConn, upstream)
	s.emitAudit(AuditEvent{
		Kind: "net", Host: host, Port: port, Mode: string(ModeSplice), Method: http.MethodConnect,
		Status: http.StatusOK, Up: up, Down: down,
		Rule: dec.Rule, Decision: auditDecision(dec),
	})
}

type httpAuditMeta struct {
	host     string
	port     int
	method   string
	path     string
	rule     string
	decision string
}

func (s *Server) handleHTTPRequest(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		err := fmt.Errorf("egress: unsupported proxy scheme %q. Try: use http or https", r.URL.Scheme)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: r.URL.Host, Mode: string(ModeHTTP), Method: r.Method,
			Path: s.Redactor.Path(r.URL.Path), Status: http.StatusForbidden,
			Rule: "invalid", Decision: string(ActionDeny), Why: err.Error(),
		})
		return r, textResponse(r, http.StatusForbidden, err.Error()+"\n")
	}
	normHost, port, err := parseURLHostPort(r.URL.Host, r.URL.Scheme)
	if err != nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: r.URL.Host, Mode: string(ModeHTTP), Method: r.Method,
			Path: s.Redactor.Path(r.URL.Path), Status: http.StatusBadRequest,
			Rule: "invalid", Decision: string(ActionDeny), Why: err.Error(),
		})
		return r, textResponse(r, http.StatusBadRequest, err.Error()+"\n")
	}
	if err := checkHostHeaderMatch(r.Host, normHost, port, r.URL.Scheme); err != nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeHTTP), Method: r.Method,
			Path: s.Redactor.Path(r.URL.Path), Status: http.StatusBadRequest,
			Rule: "host_mismatch", Decision: string(ActionDeny), Why: err.Error(),
		})
		return r, textResponse(r, http.StatusBadRequest, err.Error()+"\n")
	}

	redactedPath := s.Redactor.Path(r.URL.Path)
	dec := s.Engine.Decide(Request{
		Host: normHost, Port: port, Method: r.Method, Path: r.URL.Path, Header: r.Header,
	})
	if dec.Action != ActionAllow {
		body := s.Engine.DenyHint(normHost, port)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeHTTP), Method: r.Method,
			Path: redactedPath, Status: http.StatusForbidden, Rule: dec.Rule,
			Decision: string(ActionDeny), Why: dec.Why,
		})
		return r, textResponse(r, http.StatusForbidden, body+"\n")
	}

	addrs, err := s.Dialer.ResolveAndCheck(r.Context(), normHost, port)
	if err != nil {
		status, decision := errHTTPStatus(err)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeHTTP), Method: r.Method,
			Path: redactedPath, Status: status, Rule: "range", Decision: decision, Why: err.Error(),
		})
		return r, textResponse(r, status, err.Error()+"\n")
	}

	r = r.WithContext(withCheckedAddrs(r.Context(), addrs))
	ctx.UserData = httpAuditMeta{
		host: normHost, port: port, method: r.Method, path: redactedPath,
		rule: dec.Rule, decision: auditDecision(dec),
	}
	return r, nil
}

func (s *Server) handleHTTPResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	meta, ok := ctx.UserData.(httpAuditMeta)
	if !ok {
		return resp
	}
	if resp == nil {
		why := "upstream error"
		if ctx.Error != nil {
			why = ctx.Error.Error()
		}
		status, decision := http.StatusBadGateway, "error"
		if ctx.Error != nil {
			status, decision = errHTTPStatus(ctx.Error)
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: meta.host, Port: meta.port, Mode: string(ModeHTTP),
			Method: meta.method, Path: meta.path, Status: status,
			Rule: meta.rule, Decision: decision, Why: why,
		})
		return textResponse(ctx.Req, status, why+"\n")
	}
	var upBytes int64
	if ctx.Req != nil && ctx.Req.ContentLength > 0 {
		upBytes = ctx.Req.ContentLength
	}
	if resp.Body == nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: meta.host, Port: meta.port, Mode: string(ModeHTTP),
			Method: meta.method, Path: meta.path, Status: resp.StatusCode,
			Up: upBytes, Rule: meta.rule, Decision: meta.decision,
		})
		return resp
	}
	resp.Body = &auditReadCloser{
		ReadCloser: resp.Body,
		onDone: func(down int64) {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: meta.host, Port: meta.port, Mode: string(ModeHTTP),
				Method: meta.method, Path: meta.path, Status: resp.StatusCode,
				Up: upBytes, Down: down, Rule: meta.rule, Decision: meta.decision,
			})
		},
	}
	return resp
}
