package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

func (s *Server) needsHTTPInspection(hdr StreamHeader) (bool, error) {
	kind, spliceOnly, err := httpClass(hdr.Host)
	if err != nil {
		return false, err
	}
	constrained := s.currentEngine().hasInspectionRule(hdr.Host, hdr.Port) || s.decoyHost(hdr.Host, hdr.Port)
	if spliceOnly {
		if constrained {
			return false, errors.New("splice-only host cannot satisfy a method or path rule")
		}
		return false, nil
	}
	return constrained || kind != "", nil
}

func (s *Server) serveInspectedFlow(ctx context.Context, conn net.Conn, reader io.Reader, hdr StreamHeader, connect bool, cause error) AuditEntry {
	entry := AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "intercept", Method: "CONNECT",
		Status: http.StatusForbidden, Rule: "policy:inspection", Decision: "deny"}
	if cause != nil || !s.currentEngine().permitsIntercept(hdr.Host, hdr.Port) && !s.decoyHost(hdr.Host, hdr.Port) {
		if connect {
			entry.Down, _ = writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", RefusalLine(s.sessionRef(), hdr.Host, hdr.Port))
		}
		return s.formatAudit(entry)
	}
	ctx, _, endTunnel := s.httpTunnel(ctx, conn)
	defer endTunnel()
	if connect {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return s.formatAudit(entry)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	buffer := bufio.NewReader(reader)
	first, err := buffer.Peek(1)
	if err != nil {
		return s.formatAudit(entry)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if first[0] == tlsRecordHandshake {
		return s.serveIntercept(ctx, conn, buffer, hdr, false)
	}
	if summary := s.serveHTTP1(ctx, conn, buffer, nil, hdr, "http"); summary != nil {
		entry = *summary
	}
	return s.formatAudit(entry)
}

func (s *Server) servePlainHTTPProxy(ctx context.Context, conn net.Conn, reader io.Reader, req *http.Request, hdr StreamHeader) AuditEntry {
	if summary := s.serveHTTP1(ctx, conn, reader, req, hdr, "http"); summary != nil {
		return *summary
	}
	return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "intercept", Method: "HTTP",
		Status: http.StatusBadRequest, Rule: "http:protocol", Decision: "deny"})
}
