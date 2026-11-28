package egress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

// HandleTransparent handles a transparent TCP flow (for example from a microvm
// guest) whose destination host (or synthetic DNS IP) and port come from the
// stream header. It enforces policy, address checks, and port-443 SNI/ECH
// checks.
func (s *Server) HandleTransparent(ctx context.Context, clientConn net.Conn, rawHost string, port int) {
	defer func() { _ = clientConn.Close() }()

	host := rawHost
	if addr, err := netip.ParseAddr(rawHost); err == nil {
		if mapped, ok := s.DNS.LookupSynthetic(addr); ok {
			host = mapped
		}
	}
	normHost, err := normalizeHost(host)
	if err != nil || port < 1 || port > 65535 {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: rawHost, Port: port, Mode: string(ModeSplice),
			Status: http.StatusBadRequest, Rule: "invalid", Decision: string(ActionDeny),
			Why: "invalid transparent stream header",
		})
		return
	}

	dec := s.Engine.Decide(Request{Host: normHost, Port: port})
	if dec.Action != ActionAllow {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice),
			Status: http.StatusForbidden, Rule: dec.Rule, Decision: string(ActionDeny), Why: dec.Why,
		})
		return
	}

	addrs, err := s.Dialer.ResolveAndCheck(ctx, normHost, port)
	if err != nil {
		status, decision := errHTTPStatus(err)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice),
			Status: status, Rule: "range", Decision: decision, Why: err.Error(),
		})
		return
	}

	var replay []byte
	if port == 443 {
		ch, b, peekErr := PeekClientHello(ctx, clientConn, s.sniTimeout)
		if peekErr == nil {
			peekErr = CheckClientHello(ch, normHost)
		}
		if peekErr != nil {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice),
				Status: http.StatusForbidden, Rule: "sni", Decision: string(ActionDeny), Why: peekErr.Error(),
			})
			return
		}
		replay = b
	}

	upstream, _, err := s.Dialer.DialChecked(ctx, addrs, port)
	if err != nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice),
			Status: http.StatusBadGateway, Rule: dec.Rule, Decision: "error", Why: err.Error(),
		})
		return
	}
	defer func() { _ = upstream.Close() }()
	if len(replay) > 0 {
		if _, err := upstream.Write(replay); err != nil {
			return
		}
	}
	up, down := spliceConns(clientConn, upstream)
	s.emitAudit(AuditEvent{
		Kind: "net", Host: normHost, Port: port, Mode: string(ModeSplice),
		Status: http.StatusOK, Up: int64(len(replay)) + up, Down: down,
		Rule: dec.Rule, Decision: auditDecision(dec),
	})
}

type auditReadCloser struct {
	io.ReadCloser
	down   int64
	once   sync.Once
	onDone func(int64)
}

func (a *auditReadCloser) Read(p []byte) (int, error) {
	n, err := a.ReadCloser.Read(p)
	a.down += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("egress: read upstream body: %w", err)
	}
	return n, err //nolint:wrapcheck // io.EOF must be returned directly for io.Copy
}

func (a *auditReadCloser) Close() error {
	err := a.ReadCloser.Close()
	a.once.Do(func() { a.onDone(a.down) })
	if err != nil {
		return fmt.Errorf("egress: close upstream body: %w", err)
	}
	return nil
}

func parseConnectAuthority(authority string) (string, int, error) {
	h, p, err := net.SplitHostPort(authority)
	if err != nil {
		return "", 0, fmt.Errorf("egress: CONNECT target %q must be host:port. Try: pass host:port", authority)
	}
	port, err := parsePort(p)
	if err != nil {
		return "", 0, err
	}
	norm, err := normalizeHost(h)
	if err != nil {
		return "", 0, err
	}
	return norm, port, nil
}

func parseURLHostPort(hostport, scheme string) (string, int, error) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		port, err := parsePort(p)
		if err != nil {
			return "", 0, err
		}
		norm, err := normalizeHost(h)
		return norm, port, err
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	norm, err := normalizeHost(hostport)
	return norm, port, err
}

func checkHostHeaderMatch(reqHost, urlHost string, urlPort int, scheme string) error {
	if strings.TrimSpace(reqHost) == "" {
		return nil
	}
	hdrHost, hdrPort, err := parseURLHostPort(reqHost, scheme)
	if err != nil || hdrHost != urlHost || hdrPort != urlPort {
		return fmt.Errorf("egress: Host header %q does not match request URL %s:%d. Try: send a consistent Host header", reqHost, urlHost, urlPort)
	}
	return nil
}

func errHTTPStatus(err error) (int, string) {
	var refused *RefusedAddrError
	if errors.As(err, &refused) {
		return http.StatusForbidden, string(ActionDeny)
	}
	return http.StatusBadGateway, "error"
}

func textResponse(req *http.Request, status int, body string) *http.Response {
	protoMajor, protoMinor := 1, 1
	if req != nil && req.ProtoMajor > 0 {
		protoMajor, protoMinor = req.ProtoMajor, req.ProtoMinor
	}
	return &http.Response{
		StatusCode:    status,
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		Proto:         fmt.Sprintf("HTTP/%d.%d", protoMajor, protoMinor),
		ProtoMajor:    protoMajor,
		ProtoMinor:    protoMinor,
		Header:        http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewBufferString(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}
