package egress

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// requestHTTPTransport keeps the one session transport configuration while
// assigning every ordinary request its own uncached physical connection.
type requestHTTPTransport struct {
	server *Server
	scope  *originRequest
}

type ownedHTTPClientConn struct{ *ownedHTTPClientConnState }

type ownedHTTPClientConnState struct {
	conn *http.ClientConn
	once sync.Once
}

func (c *ownedHTTPClientConn) close() { c.once.Do(func() { _ = c.conn.Close() }) }

func (requestHTTPTransport) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (ownedHTTPClientConn) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, api.Redacted) }
func (*ownedHTTPClientConnState) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, api.Redacted)
}

func (t requestHTTPTransport) validate(req *http.Request) error {
	r := t.scope
	if req == nil || req.URL == nil || req.Header == nil || r == nil || t.server == nil || r.s != t.server ||
		t.server.transport == nil || r.ctx == nil || r.permit == nil || req.Context().Value(originRequestKey{}) != r {
		return errors.New("origin request has no exact owned transport scope")
	}
	if (r.scheme != "http" && r.scheme != "https") || req.URL.Scheme != r.scheme || req.URL.User != nil || req.URL.Opaque != "" || req.URL.Fragment != "" {
		return errors.New("origin request scheme differs from its admitted scope")
	}
	authority := net.JoinHostPort(r.request.Host, strconv.Itoa(r.request.Port))
	path, err := canonicalPath(req.URL.EscapedPath())
	if err != nil || req.URL.Host != authority || req.Host != authority || req.Method != r.request.Method || path != r.request.Path || req.URL.RawQuery != r.query {
		return errors.New("origin request route differs from its admitted scope")
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed || r.ctx.Err() != nil || req.Context().Err() != nil {
		return errors.New("origin request scope ended before connection acquisition")
	}
	return nil
}

// RoundTrip retains a fresh native connection for the whole owned response.
func (t requestHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.validate(req); err != nil {
		return nil, err
	}
	// This is the standard library's requiresHTTP1 predicate. Bound classic
	// upgrades were validated before this call. Keep that existing H1 path;
	// NewClientConn has no per-request HTTP1 selection argument.
	if webSocketHeaderToken(req.Header, "Connection", "upgrade") && strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		response, err := t.server.transport.RoundTrip(req)
		if err != nil {
			return nil, fmt.Errorf("send the owned classic WebSocket upgrade: %w", err)
		}
		return response, nil
	}
	conn, err := t.server.transport.NewClientConn(req.Context(), req.URL.Scheme, req.URL.Host)
	if err != nil {
		return nil, fmt.Errorf("open the owned origin connection: %w", err)
	}
	owned := &ownedHTTPClientConn{&ownedHTTPClientConnState{conn: conn}}
	t.scope.mu.Lock()
	if t.scope.closed || t.scope.ctx.Err() != nil || t.scope.client != nil {
		t.scope.mu.Unlock()
		owned.close()
		return nil, errors.New("origin request connection ownership changed during acquisition")
	}
	t.scope.client = owned
	t.scope.mu.Unlock()
	response, err := conn.RoundTrip(req)
	if err != nil {
		owned.close()
		return nil, fmt.Errorf("send the owned canonical origin request: %w", err)
	}
	// The same origin scope closes this connection after the complete proxy
	// response/upgrade and its finalisers, or on any owned cancellation.
	return response, nil
}
