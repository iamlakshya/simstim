package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

type readerConn struct {
	net.Conn
	reader io.Reader
}

func (c *readerConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// connListener adapts one inherited stream to http.Server. It opens no socket.
// Accept waits for the served connection's end rather than making Serve return
// before its request handlers, SSE stream or upgraded WebSocket have finished.
type connListener struct {
	conn     net.Conn
	mu       sync.Mutex
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *connListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}

func (l *connListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *connListener) Addr() net.Addr { return l.conn.LocalAddr() }

func closeHTTPConnection(conn net.Conn) {
	if tlsConn, ok := conn.(*tls.Conn); ok {
		_ = tlsConn.NetConn().Close()
		return
	}
	_ = conn.Close()
}

func serveHTTPConnection(ctx context.Context, conn net.Conn, config *tls.Config, handler http.Handler, idle func()) {
	var serverConfig *tls.Config
	if config != nil {
		// HTTP/2 setup mutates its server config. Each owned connection must
		// isolate those writes from a reused handshake/configuration template.
		serverConfig = config.Clone()
		serverConfig.NextProtos = append([]string(nil), serverConfig.NextProtos...)
	}
	listener := &connListener{conn: conn, done: make(chan struct{})}
	var requests sync.WaitGroup
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		defer requests.Done()
		handler.ServeHTTP(w, r)
	}), TLSConfig: serverConfig, ReadHeaderTimeout: handshakeTimeout,
		MaxHeaderBytes: maxHTTPHeaderBytes,
		HTTP2:          &http.HTTP2Config{MaxConcurrentStreams: maxActiveFlows},
		BaseContext:    func(net.Listener) context.Context { return ctx },
		ConnState: func(_ net.Conn, state http.ConnState) {
			if idle != nil && (state == http.StateIdle || state == http.StateClosed) {
				idle()
			}
			if state == http.StateClosed || state == http.StateHijacked {
				_ = listener.Close()
			}
		},
	}
	stop := context.AfterFunc(ctx, func() { closeHTTPConnection(conn); _ = server.Close(); _ = listener.Close() })
	defer stop()
	_ = server.Serve(listener)
	requests.Wait()
	_ = server.Close()
}

type http1Turn struct {
	forward     *httpForwarding
	done        chan struct{}
	upgraded    chan struct{}
	once        sync.Once
	upgradeOnce sync.Once
	settleOnce  sync.Once
	drain       func()
	stopRead    func()
	finishAudit func()
	refusal     string
	method      string
	target      string
	original    *requestSecretRawView
}

func (t *http1Turn) finish()  { t.once.Do(func() { close(t.done) }) }
func (t *http1Turn) upgrade() { t.upgradeOnce.Do(func() { close(t.upgraded) }) }

func (t *http1Turn) settle() {
	t.settleOnce.Do(func() {
		if t.drain != nil {
			t.drain()
		}
		if t.finishAudit != nil {
			t.finishAudit()
		}
		t.finish()
	})
}

// Raw H1 headers are checked before net/http can fold equal duplicate lengths
// or discard an absolute target's original Host. Request.Write then passes a
// canonical, properly framed request to the standard server and ReverseProxy.
// H2 uses the standard TLS server's own strict protocol parser directly.
func (s *Server) serveHTTP1(ctx context.Context, conn net.Conn, reader io.Reader, first *http.Request, hdr StreamHeader, scheme string) *AuditEntry {
	ctx, tunnel, endTunnel := s.httpTunnel(ctx, conn)
	defer endTunnel()
	serverEnd, guardEnd := net.Pipe()
	defer serverEnd.Close()
	defer guardEnd.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	forward := newHTTPForwarding()
	served := &httpServerConn{Conn: serverEnd, forward: forward}
	tunnel.setBodyStop(func() { _ = serverEnd.Close(); _ = guardEnd.Close() })
	newTurn := func() *http1Turn {
		return &http1Turn{forward: forward, done: make(chan struct{}), upgraded: make(chan struct{}),
			drain: func() { forward.wait(ctx) },
			stopRead: func() {
				_ = serverEnd.SetReadDeadline(time.Now())
				_ = guardEnd.SetWriteDeadline(time.Now())
				_ = conn.SetReadDeadline(time.Now())
			}}
	}
	var summary *httpAuditResult
	turns := make(chan *http1Turn, 1)
	var activeMu sync.Mutex
	var active *http1Turn
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		defer guardEnd.Close()
		stream := bufio.NewReaderSize(reader, maxHTTPHeaderBytes)
		reject := func(message string, original *http.Request) {
			turn := newTurn()
			turn.refusal = message
			if original != nil {
				turn.method, turn.target = original.Method, original.RequestURI
			}
			select {
			case turns <- turn:
			case <-ctx.Done():
				return
			}
			request := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"},
				Host: net.JoinHostPort(hdr.Host, strconv.Itoa(hdr.Port)), Header: make(http.Header), Close: true}
			if err := request.Write(guardEnd); err != nil {
				return
			}
			select {
			case <-turn.done:
			case <-ctx.Done():
			}
		}
		for {
			req := first
			first = nil
			if req == nil {
				_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
				parsed, raw, err := readBufferedHTTPRequest(stream)
				_ = conn.SetReadDeadline(time.Time{})
				if err != nil {
					if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
						reject(brand.Name+": malformed HTTP request. Try: send one complete request header.", nil)
					}
					return
				}
				if len(raw["Host"]) > 1 || validateRequestFraming(parsed, raw) != nil {
					reject(brand.Name+": invalid HTTP framing. Try: send one unambiguous request.", parsed)
					return
				}
				// Preserve the original Host before Request.Write canonicalises it.
				if len(raw["Host"]) != 1 {
					reject(brand.Name+": HTTP request has no Host. Try: send one authority.", parsed)
					return
				}
				host, port, err := parseHostHeader(raw["Host"][0], hdr.Port)
				if err != nil || host != hdr.Host || port != hdr.Port {
					reject(brand.Name+": Host differs from the tunnel. Try: use the tunnel's authority.", parsed)
					return
				}
				req = parsed
			}
			original := originalRequestSecretView(req)
			if _, err := canonicalHTTPRequest(req, hdr, scheme); err != nil {
				reject(brand.Name+": "+err.Error(), req)
				return
			}
			turn := newTurn()
			turn.original = &original
			select {
			case turns <- turn:
			case <-ctx.Done():
				return
			}
			if err := req.Write(guardEnd); err != nil {
				// A completed handler can stop an unfinished upload without
				// making its refusal/response lose the last forwarded bytes.
				// An invalid body also needs a read-side error at the standard
				// server, while its response write side stays open for a 502.
				turn.stopRead()
				select {
				case <-turn.done:
				case <-ctx.Done():
				}
				return
			}
			if req.Header.Get("Upgrade") == "" {
				// Like the standard H1 server's background read, observe an
				// idle peer closing while a response is still streaming. Use
				// the bounded reader and never start a pipelined turn
				// before its predecessor's forwarding fence.
				if _, err := stream.Peek(1); err != nil {
					return
				}
				if req.Close {
					return
				}
			}
			select {
			case <-turn.upgraded:
				_, _ = io.Copy(guardEnd, stream)
				return
			case <-turn.done:
				if req.Close {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		defer forward.advance(0, true)
		if _, err := io.Copy(&httpGuestWriter{Conn: conn, forward: forward}, guardEnd); err != nil {
			tunnel.abort()
		}
	}()
	serveHTTPConnection(ctx, served, nil, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case turn := <-turns:
			activeMu.Lock()
			active = turn
			activeMu.Unlock()
			summary = s.proxyHTTPRequest(w, req, hdr, scheme, turn)
		case <-ctx.Done():
		}
	}), func() {
		activeMu.Lock()
		turn := active
		active = nil
		activeMu.Unlock()
		if turn != nil {
			turn.settle()
		}
	})
	// The server's final pipe write can finish before its bytes reach the
	// guest. Drain that forwarding copy before closing an ordinary response.
	select {
	case <-copyDone:
	case <-ctx.Done():
		_ = conn.Close()
		<-copyDone
	}
	cancel()
	_ = conn.Close()
	_ = guardEnd.Close()
	<-guardDone
	if summary != nil {
		entry := summary.get()
		return &entry
	}
	return nil
}

func (s *Server) serveIntercept(ctx context.Context, conn net.Conn, reader io.Reader, hdr StreamHeader, connect bool) AuditEntry {
	ctx, _, endTunnel := s.httpTunnel(ctx, conn)
	defer endTunnel()
	entry := AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "intercept", Method: "TLS", Status: http.StatusForbidden, Rule: "tls:client_hello", Decision: "deny"}
	if connect {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return s.formatAudit(entry)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	hello, err := ReadClientHello(reader)
	if err != nil {
		return s.formatAudit(entry)
	}
	if refusal := s.checkSecretDestination(ctx, hello.RawServerName); refusal != nil {
		entry.Rule, entry.Secret = refusal.auditRule(), refusal.decoy
		return s.formatAudit(entry)
	}
	if err := CheckClientHello(hello, hdr.Host); err != nil {
		if errors.Is(err, ErrECH) {
			entry.Rule = "tls:ech"
		} else {
			entry.Rule = "tls:sni"
		}
		return s.formatAudit(entry)
	}
	leaf, err := s.ca.certificate(hdr.Host, s.now())
	if err != nil {
		entry.Rule = "tls:authority"
		return s.formatAudit(entry)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf},
		NextProtos: []string{"h2", "http/1.1"}, SessionTicketsDisabled: true}
	client := tls.Server(&readerConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(hello.Raw), reader)}, config)
	if err := client.HandshakeContext(ctx); err != nil {
		return s.formatAudit(entry)
	}
	_ = conn.SetReadDeadline(time.Time{})
	_, pendingRefusal := ctx.Value(pendingNetworkRefusalKey{}).(pendingNetworkRefusal)
	if !pendingRefusal && !s.currentEngine().permitsIntercept(hdr.Host, hdr.Port) && !s.decoyHost(hdr.Host, hdr.Port) && !s.classifierServiceInspection(s.currentEngine(), hdr.Host, hdr.Port) {
		return s.formatAudit(entry)
	}
	if client.ConnectionState().NegotiatedProtocol == "h2" {
		var summaryMu sync.Mutex
		var last *httpAuditResult
		_ = servePhysicalHTTP2(ctx, client, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			requestEntry := s.proxyHTTPRequest(w, req, hdr, "https", nil)
			summaryMu.Lock()
			last = requestEntry
			summaryMu.Unlock()
		}))
		summaryMu.Lock()
		if last != nil {
			entry = last.get()
		}
		summaryMu.Unlock()
	} else {
		if summary := s.serveHTTP1(ctx, client, client, nil, hdr, "https"); summary != nil {
			entry = *summary
		}
	}
	return s.formatAudit(entry)
}
