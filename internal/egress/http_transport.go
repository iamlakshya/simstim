package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

type originRequestKey struct{}

// originRequest belongs to one actual HTTP request, never to a CONNECT tunnel.
// Its immutable request and permit travel through the one session Transport's
// dial context, including concurrent guest HTTP/2 streams.
type originRequest struct {
	s        *Server
	request  Request
	scheme   string
	query    string
	permit   *Permit
	cancel   context.CancelFunc
	ctx      context.Context
	tunnel   *httpTunnel
	body     io.Closer
	progress *httpBodyProgress
	delivery *httpBodyProgress
	model    *modelRequest   // immutable snapshots; publication uses mu
	service  *serviceRequest // same ownership for a service body/path decision
	http2    bool
	writerMu sync.Mutex // serialises writer use with retirement before handler return
	writer   *http.ResponseController
	mu       sync.Mutex
	conn     net.Conn
	client   *ownedHTTPClientConn
	measured *measuredOriginConn
	flow     uint64
	addr     netip.Addr
	reserved bool
	closed   bool
	stopOnce sync.Once
	endOnce  sync.Once
	done     chan struct{}
}

func (r *originRequest) track() bool {
	r.writerMu.Lock()
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil || r.http2 && r.writer == nil {
		r.mu.Unlock()
		r.writerMu.Unlock()
		return false
	}
	if r.done == nil {
		r.done = make(chan struct{})
	}
	r.mu.Unlock()
	r.writerMu.Unlock()
	if r.tunnel != nil && !r.tunnel.add(r) {
		return false
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		if r.tunnel != nil {
			r.tunnel.remove(r)
		}
		return false
	}
	r.flow = r.s.flows.addHTTPRequest(activeFlow{request: r.request, permit: r.permit, check: r.allowed, cancel: r.abort, http: true, secretPending: true, done: r.done})
	tracked := r.flow != 0
	r.mu.Unlock()
	if !tracked && r.tunnel != nil {
		r.tunnel.remove(r)
	}
	return tracked
}

func (r *originRequest) allowed(eng *Engine, dialer *Dialer) bool {
	r.mu.Lock()
	closed, reserved, addr, model, service := r.closed, r.reserved, r.addr, r.model, r.service
	r.mu.Unlock()
	if closed {
		return false
	}
	if r.tunnel != nil && r.tunnel.ctx.Err() != nil {
		return false
	}
	if model != nil && model.refusal(eng, r.permit, evalRevalidate) != "" {
		return false
	}
	if service != nil && !service.allowedAtState(eng, r.permit, r.s.currentMembers()) {
		return false
	}
	if !reserved {
		return eng.evaluateModelNetwork(r.request, r.permit, evalPrecheck).Allow
	}
	return flowAllowed(eng, dialer, activeFlow{request: r.request, addr: addr, permit: r.permit})
}

func (r *originRequest) setDelivery(body *httpBodyProgress) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		body.stop()
		return false
	}
	r.delivery = body
	return true
}

func (s *Server) newHTTPTransport(roots *x509.CertPool) (*http.Transport, error) {
	if roots == nil {
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("read origin system roots: %w", err)
		}
	} else {
		roots = roots.Clone()
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            s.dialHTTPRequest,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout:    handshakeTimeout,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: maxHTTPHeaderBytes,
		Protocols:              protocols,
		ForceAttemptHTTP2:      true,
		DisableKeepAlives:      true,
		DisableCompression:     true,
	}, nil
}

func (s *Server) dialHTTPRequest(ctx context.Context, network, address string) (net.Conn, error) {
	scope, ok := ctx.Value(originRequestKey{}).(*originRequest)
	if !ok || scope.s != s || scope.permit == nil {
		return nil, errors.New("origin dial has no request permission")
	}
	// Transport may detach a dial from the handler context. Keep policy
	// cancellation attached to this exact request, including pending DNS.
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	defer cancel()
	if scope.ctx.Err() != nil {
		return nil, scope.ctx.Err()
	}
	if !scope.allowed(s.currentEngine(), s.dialer) {
		return nil, fmt.Errorf("%w: HTTP request ended before origin resolution", ErrRefusedAddr)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse origin authority: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || host != scope.request.Host || port != scope.request.Port || network != "tcp" {
		return nil, errors.New("origin dial differs from the authorised request")
	}
	conn, addr, _, err := s.dialOrigin(ctx, host, port, scope.permit, &scope.request)
	if err != nil {
		return nil, err
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.closed {
		_ = conn.Close()
		return nil, errors.New("origin request ended during its checked dial")
	}
	if scope.conn != nil {
		_ = conn.Close()
		return nil, errors.New("origin request attempted a second simultaneous connection")
	}
	scope.measured = &measuredOriginConn{Conn: conn}
	conn = &checkedHTTPConn{Conn: scope.measured, scope: scope}
	scope.conn = conn
	scope.addr = addr
	scope.permit.Commit()
	if !s.flowAllowed(s.currentEngine(), activeFlow{request: scope.request, addr: addr, permit: scope.permit}) {
		scope.cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("%w: request was revoked before origin TLS or HTTP writes", ErrRefusedAddr)
	}
	return conn, nil
}

func (r *originRequest) close() {
	r.endOnce.Do(func() {
		r.stopIO()
		if r.tunnel != nil {
			r.tunnel.remove(r)
		}
		if r.flow != 0 {
			r.s.flows.remove(r.flow)
		}
		for _, ended := range r.permit.Close() {
			r.s.emitOnceEnd(ended)
		}
		r.mu.Lock()
		if r.done != nil {
			close(r.done)
		}
		r.mu.Unlock()
	})
}

func (r *originRequest) stopIO() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		conn := r.conn
		client := r.client
		delivery := r.delivery
		r.mu.Unlock()
		if r.progress != nil {
			r.progress.stop()
		}
		if delivery != nil {
			delivery.stop()
		}
		if conn != nil {
			_ = conn.Close()
		}
		if client != nil {
			client.close()
		}
		// A decoder may attach cancellation to Close. Its owned socket must
		// already be closed before cancellation can enter decoder teardown.
		r.cancel()
		if r.http2 && r.body != nil {
			_ = r.body.Close()
		}
		if r.progress != nil {
			r.progress.wait()
		}
		if delivery != nil {
			delivery.wait()
		}
	})
}

func (r *originRequest) abort() {
	r.writerMu.Lock()
	writer := r.writer
	if writer != nil {
		// A stalled H2 DATA write is independent of origin reads. End this
		// stream's write before joining its handler, without closing siblings.
		_ = writer.SetWriteDeadline(time.Now())
	}
	r.writerMu.Unlock()
	if !r.http2 && r.tunnel != nil {
		// H1 has one active turn. Close its owned pipes before touching a
		// server Body.Close, which is allowed to drain untrusted input.
		r.tunnel.abort()
	}
	r.stopIO()
}

type checkedHTTPConn struct {
	net.Conn
	scope *originRequest
}

func (c *checkedHTTPConn) Write(p []byte) (int, error) {
	c.scope.mu.Lock()
	closed := c.scope.closed
	c.scope.mu.Unlock()
	if closed {
		// Native TLS cleanup can write close-notify from inside stopIO.
		// A closed owner refuses that write without recursively aborting it.
		return 0, net.ErrClosed
	}
	if !c.scope.allowed(c.scope.s.currentEngine(), c.scope.s.dialer) {
		// TLS may hold its write lock around this callback. Return the
		// refusal before the outer request finaliser closes its ClientConn.
		return 0, fmt.Errorf("%w: HTTP request was revoked before an origin write", ErrRefusedAddr)
	}
	return c.Conn.Write(p) //nolint:wrapcheck // preserve the checked net.Conn result
}
