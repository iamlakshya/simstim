package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

type rawSecretDestinationKey struct{}

func originalSecretDestination(ctx context.Context, fallback string) string {
	if raw, ok := ctx.Value(rawSecretDestinationKey{}).(string); ok {
		return raw
	}
	return fallback
}

type opaqueTLSKey struct{}
type opaqueTLSHello struct{ raw []byte }

func (s *Server) checkSecretDestination(ctx context.Context, original string) *requestSecretFailure {
	if err := s.policyMu.LockContext(ctx); err != nil {
		detail := memberError("cancelled", "destination admission was cancelled", "Retry after the approved operation finishes.")
		return &requestSecretFailure{requestFailureDetail: detail, detail: detail, status: http.StatusForbidden}
	}
	defer s.policyMu.Unlock()
	return s.checkSecretDestinationLocked(original)
}

// The local CONNECT reply permits only a bounded ClientHello read. It neither
// reserves the once grant nor opens a TCP connection or resolves an origin.
func (s *Server) preflightOpaqueTLS(ctx context.Context, conn net.Conn, reader io.Reader, hdr StreamHeader, request Request, connect bool) (context.Context, *AuditEntry) {
	if hdr.Port != 443 {
		return ctx, nil
	}
	entry := AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: request.Method, Status: http.StatusForbidden, Decision: "deny"}
	if decision := s.currentEngine().evaluate(request, nil, evalPrecheck); !decision.Allow {
		entry.Rule = decision.Rule
		if connect {
			entry.Down, _ = writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", RefusalLine(s.sessionRef(), hdr.Host, hdr.Port))
		}
		formatted := s.formatAudit(entry)
		return ctx, &formatted
	}
	if connect {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			entry.Rule = "http:cancelled"
			formatted := s.formatAudit(entry)
			return ctx, &formatted
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	hello, err := ReadClientHello(reader)
	_ = conn.SetReadDeadline(time.Time{})
	entry.Rule = "tls:client_hello"
	if err == nil {
		if refusal := s.checkSecretDestination(ctx, hello.RawServerName); refusal != nil {
			entry.Rule, entry.Secret = refusal.auditRule(), refusal.decoy
		} else if err = CheckClientHello(hello, hdr.Host); err == nil {
			return context.WithValue(ctx, opaqueTLSKey{}, &opaqueTLSHello{raw: hello.Raw}), nil
		} else if errors.Is(err, ErrECH) {
			entry.Rule = "tls:ech"
		} else {
			entry.Rule = "tls:sni"
		}
	}
	formatted := s.formatAudit(entry)
	return ctx, &formatted
}
