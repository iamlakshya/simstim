package egress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

var headerSanitiser = strings.NewReplacer("\r", " ", "\n", " ", `"`, "'", `\`, "")

func writeHTTPStatus(w io.Writer, code int, statusText, bodyLine string) (int64, error) {
	cleanLine := strings.NewReplacer("\r", " ", "\n", " ").Replace(bodyLine)
	body := cleanLine + "\n"
	details := headerSanitiser.Replace(cleanLine)
	hdr := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nProxy-Status: %s; details=\"%s\"\r\nContent-Length: %d\r\nConnection: close\r\nX-Should-Retry: false\r\n\r\n%s",
		code, statusText, brand.Name, details, len(body), body)
	n, err := io.WriteString(w, hdr)
	return int64(n), err
}

const (
	maxHTTPHeaderBytes = 32 << 10
	handshakeTimeout   = 15 * time.Second
	maxActiveFlows     = 128
)

// ServeFlow handles one egress stream from the guest and returns its AuditEntry.
func (s *Server) ServeFlow(ctx context.Context, conn net.Conn, hdr StreamHeader) AuditEntry {
	ctx, finish, err := s.beginSecretCarrier(ctx, conn, hdr)
	if err != nil {
		_ = conn.Close()
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Rule: "secret_lifetime", Status: http.StatusForbidden, Decision: "deny"})
	}
	defer finish()
	if refusal := s.checkSecretDestination(ctx, originalSecretDestination(ctx, hdr.Host)); refusal != nil {
		_ = conn.Close()
		entry := s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Rule: refusal.Code, Status: http.StatusForbidden, Decision: "deny"})
		s.emitAudit(entry)
		return entry
	}
	audited := new(atomic.Bool)
	ctx = context.WithValue(ctx, inspectedAuditKey{}, audited)
	p := s.currentEngine().NewPermit()
	entry := s.serveFlow(ctx, conn, hdr, p)
	if !audited.Load() {
		s.emitAudit(entry)
	}
	for _, ended := range p.Close() {
		s.emitOnceEnd(ended)
	}
	return entry
}

func (s *Server) serveFlow(ctx context.Context, conn net.Conn, hdr StreamHeader, p *Permit) AuditEntry {
	defer conn.Close()
	if hdr.Via == "transparent" {
		return s.serveTransparent(ctx, conn, hdr, p)
	}
	return s.serveExplicit(ctx, conn, hdr, p)
}

func (s *Server) serveExplicit(ctx context.Context, conn net.Conn, hdr StreamHeader, p *Permit) AuditEntry {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	req, rawHdr, tail, err := readInitialHTTPRequest(conn)
	if err != nil {
		down, _ := writeHTTPStatus(conn, http.StatusBadRequest, "Bad Request", brand.Name+": malformed proxy request")
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Status: http.StatusBadRequest, Down: down, Rule: "protocol", Decision: "deny"})
	}
	_ = conn.SetReadDeadline(time.Time{})

	if err := checkSmugglingAndHost(req, rawHdr, hdr); err != nil {
		down, _ := writeHTTPStatus(conn, http.StatusBadRequest, "Bad Request", brand.Name+": "+err.Error())
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Path: requestAuditPath(req), Status: http.StatusBadRequest, Down: down, Rule: "protocol", Decision: "deny"})
	}
	if req.Method == http.MethodConnect {
		if refusal := s.checkSecretDestination(ctx, req.RequestURI); refusal != nil {
			down, _ := writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", brand.Name+": "+refusal.Message+". Try: "+refusal.Hint)
			return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Status: http.StatusForbidden, Down: down, Rule: refusal.Code, Decision: "deny"})
		}
		return s.serveConnect(ctx, conn, tail, hdr, p)
	}
	return s.servePlainHTTPProxy(ctx, conn, tail, req, hdr)
}

func readInitialHTTPRequest(conn net.Conn) (*http.Request, textproto.MIMEHeader, io.Reader, error) {
	// Peek headers first to detect duplicate Host headers before net/http folds them.
	br := bufio.NewReaderSize(&io.LimitedReader{R: conn, N: maxHTTPHeaderBytes}, 8<<10)
	rawBytes, err := readUntilHeaderEnd(br)
	if err != nil {
		return nil, nil, nil, err
	}
	rawHdr, err := parseRawHeaders(rawBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	// Preserve read-ahead bytes and let net/http decode the real body, including
	// chunk framing and trailers. The same reader retains pipelined TLS bytes
	// after CONNECT. Only the header acquisition is subject to the header cap.
	tail := io.MultiReader(bytes.NewReader(drainBuffered(br)), conn)
	reader := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(rawBytes), tail), 8<<10)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse HTTP request: %w", err)
	}
	return req, rawHdr, reader, nil
}

func readUntilHeaderEnd(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for len(buf) < maxHTTPHeaderBytes {
		line, err := br.ReadSlice('\n')
		buf = append(buf, line...)
		if bytes.HasSuffix(buf, []byte("\r\n\r\n")) || bytes.HasSuffix(buf, []byte("\n\n")) {
			return buf, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read HTTP header line: %w", err)
		}
	}
	return nil, errors.New("HTTP header too large")
}

func parseRawHeaders(raw []byte) (textproto.MIMEHeader, error) {
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	if _, err := tp.ReadLine(); err != nil {
		return nil, fmt.Errorf("read HTTP request line: %w", err)
	}
	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("read MIME headers: %w", err)
	}
	return hdr, nil
}

func drainBuffered(br *bufio.Reader) []byte {
	n := br.Buffered()
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	_, _ = io.ReadFull(br, out)
	return out
}

func checkSmugglingAndHost(req *http.Request, rawHdr textproto.MIMEHeader, hdr StreamHeader) error {
	if len(rawHdr["Host"]) > 1 {
		return errors.New("duplicate Host header")
	}
	if err := validateRequestFraming(req, rawHdr); err != nil {
		return err
	}
	reqHost, reqPort, err := extractRequestTarget(req)
	if err != nil {
		return err
	}
	if reqHost != hdr.Host || reqPort != hdr.Port {
		return errors.New("request target does not match stream header")
	}
	// net/http uses the absolute URL's authority as req.Host and ignores a
	// supplied Host field. Check that original field before forwarding too.
	if values := rawHdr["Host"]; len(values) == 1 {
		hHost, hPort, err := parseHostHeader(values[0], hdr.Port)
		if err != nil || hHost != hdr.Host || hPort != hdr.Port {
			return errors.New("request Host header does not match target. Try: use the same authority in the URL and Host field")
		}
	}
	if req.Host != "" {
		hHost, hPort, err := parseHostHeader(req.Host, hdr.Port)
		if err != nil || hHost != hdr.Host || hPort != hdr.Port {
			return errors.New("request Host header does not match target")
		}
	}
	return nil
}

func extractRequestTarget(req *http.Request) (string, int, error) {
	u := req.URL
	if u == nil || strings.Contains(req.RequestURI, "#") || u.Opaque != "" || u.User != nil {
		return "", 0, errors.New("invalid proxy request target. Try: use an absolute HTTP URL without user information or fragments")
	}
	if req.Method == http.MethodConnect {
		return parseHostHeader(req.RequestURI, 443)
	}
	if !strings.EqualFold(u.Scheme, "http") || u.Host == "" {
		return "", 0, errors.New("expected absolute http URL")
	}
	return parseHostHeader(u.Host, 80)
}

func parseHostHeader(hostPort string, defPort int) (string, int, error) {
	base, port, err := splitTargetPort(strings.TrimSpace(hostPort))
	if err != nil {
		return "", 0, err
	}
	norm, err := NormaliseHost(base)
	if err != nil {
		return "", 0, err
	}
	if port == 0 {
		port = defPort
	}
	return norm, port, nil
}

func requestAuditPath(req *http.Request) string {
	if req == nil || req.Method == http.MethodConnect {
		return ""
	}
	return req.RequestURI
}

func (s *Server) serveConnect(ctx context.Context, conn net.Conn, clientReader io.Reader, hdr StreamHeader, p *Permit) AuditEntry {
	if inspect, err := s.secretCarrierInspection(ctx, hdr); inspect || err != nil {
		return s.serveInspectedFlow(ctx, conn, clientReader, hdr, true, err)
	}
	request := Request{Host: hdr.Host, Port: hdr.Port, Method: http.MethodConnect}
	var refused *AuditEntry
	ctx, refused = s.preflightOpaqueTLS(ctx, conn, clientReader, hdr, request, true)
	if refused != nil {
		return *refused
	}
	eng := s.currentEngine()
	dec := eng.DecideWithPermit(request, p)
	if !dec.Allow {
		var down int64
		if hdr.Port != 443 {
			down, _ = writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", RefusalLine(s.sessionRef(), hdr.Host, hdr.Port))
		}
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: http.MethodConnect, Status: http.StatusForbidden, Down: down, Rule: dec.Rule, Decision: "deny"})
	}
	upstream, addr, rangeRule, err := s.dialOrigin(ctx, hdr.Host, hdr.Port, p, &request)
	if err != nil {
		if errors.Is(err, ErrRefusedAddr) {
			var down int64
			if hdr.Port != 443 {
				down, _ = writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", RefusalLine(s.sessionRef(), hdr.Host, hdr.Port))
			}
			return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: http.MethodConnect, Status: http.StatusForbidden, Down: down, Rule: rangeRule, Decision: "deny"})
		}
		var down int64
		if hdr.Port != 443 {
			down, _ = writeHTTPStatus(conn, http.StatusBadGateway, "Bad Gateway", brand.Name+": upstream connection failed")
		}
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: http.MethodConnect, Status: http.StatusBadGateway, Down: down, Rule: dec.Rule, Decision: "allow"})
	}
	defer upstream.Close()

	p.Commit()
	return s.verifyAndSplice(ctx, conn, clientReader, upstream, hdr, http.MethodConnect, "", dec.Rule, addr, p)
}

func (s *Server) serveTransparent(ctx context.Context, conn net.Conn, hdr StreamHeader, p *Permit) AuditEntry {
	if inspect, err := s.secretCarrierInspection(ctx, hdr); inspect || err != nil {
		return s.serveInspectedFlow(ctx, conn, conn, hdr, false, err)
	}
	request := Request{Host: hdr.Host, Port: hdr.Port, Method: "TCP"}
	var refused *AuditEntry
	ctx, refused = s.preflightOpaqueTLS(ctx, conn, conn, hdr, request, false)
	if refused != nil {
		return *refused
	}
	eng := s.currentEngine()
	dec := eng.DecideWithPermit(request, p)
	if !dec.Allow {
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: "TCP", Status: http.StatusForbidden, Rule: dec.Rule, Decision: "deny"})
	}
	upstream, addr, rangeRule, err := s.dialOrigin(ctx, hdr.Host, hdr.Port, p, &request)
	if err != nil {
		if errors.Is(err, ErrRefusedAddr) {
			return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: "TCP", Status: http.StatusForbidden, Rule: rangeRule, Decision: "deny"})
		}
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: "TCP", Status: http.StatusBadGateway, Rule: dec.Rule, Decision: "allow"})
	}
	defer upstream.Close()
	p.Commit()
	return s.verifyAndSplice(ctx, conn, conn, upstream, hdr, "TCP", "", dec.Rule, addr, p)
}

func (s *Server) verifyAndSplice(ctx context.Context, conn net.Conn, clientReader io.Reader, upstream net.Conn, hdr StreamHeader, method, path, rule string, addr netip.Addr, p *Permit) AuditEntry {
	request := Request{Host: hdr.Host, Port: hdr.Port, Method: method, Path: path}
	id, cancelFlow := s.registerSecretFlow(ctx, conn, upstream, request, addr, p)
	defer s.flows.remove(id)
	if !s.flowAllowed(s.currentEngine(), activeFlow{request: request, addr: addr, permit: p}) {
		cancelFlow()
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: method, Path: path, Status: http.StatusForbidden, Rule: "revoked", Decision: "deny"})
	}
	if method == http.MethodConnect && hdr.Port != 443 {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: method, Status: http.StatusOK, Rule: rule, Decision: "allow"})
		}
	}
	var preRead []byte
	if hdr.Port == 443 {
		checked, ok := ctx.Value(opaqueTLSKey{}).(*opaqueTLSHello)
		if !ok || len(checked.raw) == 0 {
			return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: method, Status: http.StatusForbidden, Rule: "tls:client_hello", Decision: "deny"})
		}
		preRead = checked.raw
	}

	var upBytes int64
	if len(preRead) > 0 {
		_ = upstream.SetWriteDeadline(time.Now().Add(handshakeTimeout))
		n, err := upstream.Write(preRead)
		_ = upstream.SetWriteDeadline(time.Time{})
		upBytes += int64(n)
		if err != nil {
			return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: method, Path: path, Status: http.StatusBadGateway, Up: upBytes, Rule: rule, Decision: "allow"})
		}
	}
	upMore, downBytes := spliceConns(conn, clientReader, upstream)
	return s.formatAudit(AuditEntry{
		Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: method, Path: path,
		Status: http.StatusOK, Up: upBytes + upMore, Down: downBytes, Rule: rule, Decision: "allow",
	})
}

func (s *Server) servePlainHTTP(ctx context.Context, conn net.Conn, req *http.Request, hdr StreamHeader, p *Permit) AuditEntry {
	auditPath := req.RequestURI
	rawPath := req.URL.EscapedPath()
	if req.URL.Path == "" && req.URL.RawPath == "" {
		rawPath = "/"
	}
	cleanPath, err := canonicalPath(rawPath)
	if err != nil {
		down, _ := writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", brand.Name+": invalid request path. Try: use a canonical URL path.")
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Path: auditPath, Status: http.StatusForbidden, Down: down, Rule: "invalid", Decision: "deny"})
	}
	eng := s.currentEngine()
	request := Request{Host: hdr.Host, Port: hdr.Port, Method: req.Method, Path: cleanPath}
	dec := eng.DecideWithPermit(request, p)
	if !dec.Allow {
		down, _ := writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", RefusalLine(s.sessionRef(), hdr.Host, hdr.Port))
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Path: auditPath, Status: http.StatusForbidden, Down: down, Rule: dec.Rule, Decision: "deny"})
	}
	upstream, addr, rangeRule, err := s.dialOrigin(ctx, hdr.Host, hdr.Port, p, &request)
	if err != nil {
		if errors.Is(err, ErrRefusedAddr) {
			down, _ := writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", RefusalLine(s.sessionRef(), hdr.Host, hdr.Port))
			return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Path: auditPath, Status: http.StatusForbidden, Down: down, Rule: rangeRule, Decision: "deny"})
		}
		down, _ := writeHTTPStatus(conn, http.StatusBadGateway, "Bad Gateway", brand.Name+": upstream connection failed")
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Path: auditPath, Status: http.StatusBadGateway, Down: down, Rule: dec.Rule, Decision: "allow"})
	}
	defer upstream.Close()

	p.Commit()
	id, cancelFlow := s.registerFlow(conn, upstream, request, addr, p)
	defer s.flows.remove(id)
	if !s.flowAllowed(s.currentEngine(), activeFlow{request: request, addr: addr, permit: p}) {
		cancelFlow()
		return s.formatAudit(AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Path: auditPath, Status: http.StatusForbidden, Rule: "revoked", Decision: "deny"})
	}

	req.URL.Path, req.URL.RawPath, req.URL.Opaque = cleanPath, "", ""
	upBytes, status, downBytes := forwardPlainHTTP(conn, upstream, req)
	return s.formatAudit(AuditEntry{
		Host: hdr.Host, Port: hdr.Port, Mode: "explicit", Method: req.Method, Path: auditPath,
		Status: status, Up: upBytes, Down: downBytes, Rule: dec.Rule, Decision: "allow",
	})
}

func forwardPlainHTTP(client net.Conn, upstream net.Conn, req *http.Request) (up int64, status int, down int64) {
	req.RequestURI = ""
	req.URL.Scheme, req.URL.Host = "", ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Connection", "Keep-Alive", "Upgrade", "TE"} {
		req.Header.Del(h)
	}
	req.Close = true
	cw := &countingWriter{w: upstream}
	if err := req.Write(cw); err != nil {
		d, _ := writeHTTPStatus(client, http.StatusBadGateway, "Bad Gateway", brand.Name+": failed to write upstream request")
		return cw.n.Load(), http.StatusBadGateway, d
	}
	halfClose(upstream)

	br := bufio.NewReader(upstream)
	line, err := br.ReadString('\n')
	if err != nil {
		d, _ := writeHTTPStatus(client, http.StatusBadGateway, "Bad Gateway", brand.Name+": empty upstream response")
		return cw.n.Load(), http.StatusBadGateway, d
	}
	status = parseStatusLine(line)
	n1, _ := io.WriteString(client, line)
	n2, _ := io.Copy(client, br)
	halfClose(client)
	return cw.n.Load(), status, int64(n1) + n2
}

func parseStatusLine(line string) int {
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		if code, err := strconv.Atoi(fields[1]); err == nil && code >= 100 && code <= 999 {
			return code
		}
	}
	return http.StatusBadGateway
}

type countingWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err //nolint:wrapcheck // an io.Writer passes through its underlying Write error
}

func halfClose(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func spliceConns(client net.Conn, clientReader io.Reader, upstream net.Conn) (up, down int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		up, _ = io.Copy(upstream, clientReader)
		halfClose(upstream)
	}()
	go func() {
		defer wg.Done()
		down, _ = io.Copy(client, upstream)
		halfClose(client)
	}()
	wg.Wait()
	return up, down
}
