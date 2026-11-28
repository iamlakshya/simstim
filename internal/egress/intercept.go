package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type interceptStateKey struct{}

type interceptReqState struct {
	host         string
	port         int
	canon        CanonRequest
	redactedPath string
	decRule      string
	decAudit     string
	upReader     *uploadBudgetReader
	reqBytes     int64
	audited      atomic.Bool
}

type interceptTarget struct {
	host       string
	port       int
	allowSlash bool
}

func (s *Server) buildInterceptProxy(tr *http.Transport) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:      tr,
		FlushInterval:  -1, // stream SSE and chunked responses immediately
		Rewrite:        s.rewriteInterceptRequest,
		ModifyResponse: s.modifyInterceptResponse,
		ErrorHandler:   s.handleInterceptError,
	}
}

func (s *Server) hijackInterceptConnect(ctx context.Context, clientConn net.Conn, host string, port int, addrs []netip.Addr) {
	defer func() { _ = clientConn.Close() }()

	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	ch, replay, err := PeekClientHello(ctx, clientConn, s.sniTimeout)
	if err == nil {
		err = CheckClientHello(ch, host)
	}
	if err != nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: host, Port: port, Mode: string(ModeIntercept), Method: http.MethodConnect,
			Status: http.StatusForbidden, Rule: "sni", Decision: string(ActionDeny), Why: err.Error(),
		})
		return
	}
	s.serveInterceptTLS(ctx, &replayConn{Conn: clientConn, buf: bytes.NewReader(replay)}, host, port, addrs)
}

func (s *Server) serveInterceptTransparent(ctx context.Context, clientConn net.Conn, replay []byte, host string, port int, addrs []netip.Addr) {
	s.serveInterceptTLS(ctx, &replayConn{Conn: clientConn, buf: bytes.NewReader(replay)}, host, port, addrs)
}

func (s *Server) serveInterceptTLS(ctx context.Context, rawConn net.Conn, host string, port int, initialAddrs []netip.Addr) {
	tlsCfg := s.CA.TLSConfigForHost(host, true)
	tlsConn := tls.Server(&tlsCloseConn{
		Conn:     rawConn,
		canceled: func() bool { return ctx.Err() != nil },
	}, tlsCfg)
	defer func() { _ = tlsConn.Close() }()

	hsCtx, cancel := context.WithTimeout(ctx, s.sniTimeout)
	err := tlsConn.HandshakeContext(hsCtx)
	cancel()
	if err != nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: host, Port: port, Mode: string(ModeIntercept),
			Status: http.StatusForbidden, Rule: "tls", Decision: string(ActionDeny), Why: err.Error(),
		})
		return
	}

	target := interceptTarget{
		host:       host,
		port:       port,
		allowSlash: !s.Engine.HasPathRule(host, port),
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serveInterceptedRequest(w, r, target, initialAddrs)
	})

	var serveConn net.Conn
	var vconn *validatingHTTP1Conn
	var protos http.Protocols
	if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
		protos.SetHTTP2(true)
		serveConn = tlsConn
	} else {
		protos.SetHTTP1(true)
		vconn = newValidatingHTTP1Conn(tlsConn, func(rejectErr error) {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: host, Port: port, Mode: string(ModeIntercept),
				Status: http.StatusBadRequest, Rule: "smuggle", Decision: string(ActionDeny),
				Why: rejectErr.Error(),
			})
		})
		serveConn = vconn
	}

	lis := newSingleConnListener(serveConn)
	httpSrv := &http.Server{
		Handler:           handler,
		Protocols:         &protos,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 15 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ConnState: func(_ net.Conn, st http.ConnState) {
			if st == http.StateHijacked && vconn != nil {
				vconn.enablePassthrough()
			}
			if st == http.StateClosed || st == http.StateHijacked {
				_ = lis.Close()
			}
		},
	}
	stop := context.AfterFunc(ctx, func() {
		_ = httpSrv.Close()
	})
	defer stop()
	_ = httpSrv.Serve(lis)
}

func (s *Server) serveInterceptedRequest(w http.ResponseWriter, r *http.Request, target interceptTarget, initialAddrs []netip.Addr) {
	allowSlash := !s.Engine.HasPathRule(target.host, target.port)
	canon, err := CanonicalizeRequest(r, target.host, target.port, "https", allowSlash)
	if err != nil {
		status := http.StatusBadRequest
		rule := "smuggle"
		if errors.Is(err, ErrQueryTooLong) {
			status = http.StatusRequestURITooLong
			rule = "query_too_long"
		} else if errors.Is(err, ErrHostMismatch) {
			rule = "host_mismatch"
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: target.host, Port: target.port, Mode: string(ModeIntercept),
			Method: r.Method, Path: s.Redactor.Path(r.URL.Path),
			Status: status, Rule: rule, Decision: string(ActionDeny), Why: err.Error(),
		})
		http.Error(w, err.Error(), status)
		return
	}

	redactedPath := s.Redactor.Path(canon.Path)
	dec := s.Engine.DecideHTTP(Request{
		Host:   target.host,
		Port:   target.port,
		Method: canon.Method,
		Path:   canon.Path,
		Header: r.Header,
	})
	if dec.Action != ActionAllow {
		drainSmallBody(r)
		body := dec.Why
		if body == "" {
			body = s.denyBody(target.host, target.port, dec)
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: target.host, Port: target.port, Mode: string(ModeIntercept),
			Method: canon.Method, Path: redactedPath,
			Status: http.StatusForbidden, Rule: dec.Rule, Decision: string(ActionDeny), Why: dec.Why,
		})
		http.Error(w, body, http.StatusForbidden)
		return
	}

	if used, limit, ok := s.checkUploadBudget(target.host); !ok {
		drainSmallBody(r)
		body := uploadBudgetMessage(s.Engine.Session(), target.host, used, limit)
		s.auditUploadBudget(target.host, target.port, string(ModeIntercept), canon.Method, redactedPath, 0, 0, used, limit)
		http.Error(w, body, http.StatusForbidden)
		return
	}

	addrs := initialAddrs
	if len(addrs) == 0 {
		resolved, resErr := s.Dialer.ResolveAndCheck(r.Context(), target.host, target.port)
		if resErr != nil {
			status, decision := errHTTPStatus(resErr)
			s.emitAudit(AuditEvent{
				Kind: "net", Host: target.host, Port: target.port, Mode: string(ModeIntercept),
				Method: canon.Method, Path: redactedPath,
				Status: status, Rule: "range", Decision: decision, Why: resErr.Error(),
			})
			http.Error(w, resErr.Error(), status)
			return
		}
		addrs = resolved
	}

	state := &interceptReqState{
		host:         target.host,
		port:         target.port,
		canon:        canon,
		redactedPath: redactedPath,
		decRule:      dec.Rule,
		decAudit:     auditDecision(dec),
	}

	reqCtx := withCheckedAddrs(r.Context(), addrs)
	reqCtx = context.WithValue(reqCtx, interceptStateKey{}, state)
	reqCopy := r.Clone(reqCtx)
	if reqCopy.Body != nil && s.UploadBudget != nil && s.UploadBudget.Limit() > 0 {
		state.upReader = &uploadBudgetReader{rc: reqCopy.Body, budget: s.UploadBudget, host: target.host}
		reqCopy.Body = state.upReader
	} else if reqCopy.ContentLength > 0 {
		state.reqBytes = reqCopy.ContentLength
	}
	s.interceptProxy.ServeHTTP(w, reqCopy)
}

func (s *Server) rewriteInterceptRequest(pr *httputil.ProxyRequest) {
	state, ok := pr.In.Context().Value(interceptStateKey{}).(*interceptReqState)
	if !ok {
		return
	}
	out := pr.Out
	out.Method = state.canon.Method
	// Copy exact canonical URL fields directly so canonical Path, WireRawPath
	// and RawQuery go upstream unchanged.
	u := *state.canon.URL
	out.URL = &u
	out.Host = u.Host
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
}

func (s *Server) modifyInterceptResponse(resp *http.Response) error {
	state, ok := resp.Request.Context().Value(interceptStateKey{}).(*interceptReqState)
	if !ok {
		return nil
	}
	upBytes := state.reqBytes
	if state.upReader != nil && state.upReader.uploaded > 0 {
		upBytes = state.upReader.uploaded
	}
	emit := func(down int64) {
		if state.audited.Swap(true) {
			return
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: state.host, Port: state.port, Mode: string(ModeIntercept),
			Method: state.canon.Method, Path: state.redactedPath,
			Status: resp.StatusCode, Up: upBytes, Down: down,
			Rule: state.decRule, Decision: state.decAudit,
		})
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		emit(0)
		return nil
	}
	resp.Body = &auditReadCloser{
		ReadCloser: resp.Body,
		onDone:     emit,
	}
	return nil
}

func (s *Server) handleInterceptError(w http.ResponseWriter, r *http.Request, err error) {
	state, ok := r.Context().Value(interceptStateKey{}).(*interceptReqState)
	if !ok {
		http.Error(w, "egress intercept error", http.StatusBadGateway)
		return
	}
	if state.upReader != nil && (state.upReader.exceeded || errors.Is(err, errUploadBudgetExceeded)) {
		body := uploadBudgetMessage(s.Engine.Session(), state.host, state.upReader.used, state.upReader.limit)
		if !state.audited.Swap(true) {
			s.auditUploadBudget(state.host, state.port, string(ModeIntercept), state.canon.Method, state.redactedPath, state.upReader.uploaded, 0, state.upReader.used, state.upReader.limit)
		}
		http.Error(w, body, http.StatusForbidden)
		return
	}
	status, decision := errHTTPStatus(err)
	if !state.audited.Swap(true) {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: state.host, Port: state.port, Mode: string(ModeIntercept),
			Method: state.canon.Method, Path: state.redactedPath,
			Status: status, Rule: state.decRule, Decision: decision, Why: err.Error(),
		})
	}
	http.Error(w, fmt.Sprintf("egress: upstream request to %s failed: %v. Try: check upstream connectivity and TLS certificates", state.host, err), status)
}

const maxDrainRejectBodyBytes = 64 << 10

// drainSmallBody reads and discards a small known-length request body before
// replying with 403/400 on HTTP/1.1 so closing the connection sends a clean
// TCP FIN rather than a TCP RST that can hide the refusal from strict clients.
func drainSmallBody(r *http.Request) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return
	}
	if r.ContentLength > 0 && r.ContentLength <= maxDrainRejectBodyBytes {
		_, _ = io.CopyN(io.Discard, r.Body, r.ContentLength)
	}
}

// tlsCloseConn clears any expired write deadline before closing the underlying
// connection. crypto/tls.Conn.closeNotify sets SetWriteDeadline(time.Now())
// immediately before calling c.conn.Close() so subsequent writes fail, but on
// link.Stream an expired write deadline makes Close skip its linger and reset
// the stream, dropping already-queued HTTP response bytes and close_notify.
type tlsCloseConn struct {
	net.Conn
	canceled func() bool
}

func (c *tlsCloseConn) Close() error {
	if c.canceled == nil || !c.canceled() {
		_ = c.SetWriteDeadline(time.Time{})
	}
	return c.Conn.Close() //nolint:wrapcheck // net.Conn.Close preserves underlying error directly
}

type replayConn struct {
	net.Conn
	buf io.Reader
}

func (c *replayConn) Read(p []byte) (int, error) {
	if c.buf != nil {
		n, err := c.buf.Read(p)
		if errors.Is(err, io.EOF) {
			c.buf = nil
			if n > 0 {
				return n, nil
			}
			return c.Conn.Read(p) //nolint:wrapcheck // net.Conn.Read preserves io.EOF and net errors
		}
		return n, err //nolint:wrapcheck // net.Conn.Read preserves io.EOF and net errors
	}
	return c.Conn.Read(p) //nolint:wrapcheck // net.Conn.Read preserves io.EOF and net errors
}

type singleConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{
		conn:   conn,
		closed: make(chan struct{}),
	}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return listenerAddr("intercept-tls")
}
