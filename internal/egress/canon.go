package egress

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const (
	// MaxQueryBytes caps raw query strings on intercepted requests at 8 KiB (spec 7.2).
	MaxQueryBytes = 8192

	// MaxHTTP1HeadBytes caps raw HTTP/1.x request head bytes inspected before parsing.
	MaxHTTP1HeadBytes = 64 << 10

	// MaxDecodedBodyBytes caps decompressed request or response bodies at 64 MiB (spec 7.2).
	MaxDecodedBodyBytes = 64 << 20
)

var (
	// ErrRequestSmuggling signals a malformed or ambiguous HTTP request refused
	// to prevent request smuggling.
	ErrRequestSmuggling = errors.New("request smuggling rejected")
	// ErrQueryTooLong signals a query string exceeding MaxQueryBytes.
	ErrQueryTooLong = errors.New("query string exceeds 8 KiB limit")
	// ErrHostMismatch signals a Host header, :authority or absolute target that
	// does not match the TLS SNI / CONNECT host.
	ErrHostMismatch = errors.New("host does not match TLS SNI")
	// ErrBodyTooLarge signals a compressed body decoding past MaxDecodedBodyBytes.
	ErrBodyTooLarge = errors.New("decoded body exceeds 64 MiB limit")
	// ErrUnsupportedEncoding signals a Content-Encoding other than identity, gzip or deflate.
	ErrUnsupportedEncoding = errors.New("unsupported content encoding")
)

// CanonRequest is the validated, canonical representation of an HTTP request
// matched against policy and forwarded upstream (spec 7.2).
type CanonRequest struct {
	Method      string
	Host        string
	Port        int
	Scheme      string
	Path        string
	WireRawPath string
	RawQuery    string
	URL         *url.URL
}

// CanonicalizePath validates rawPath and escapedPath against traversal,
// backslashes, control characters and invalid percent-encoding. When
// allowEncodedSlash is false (any host with path rules), %2f is refused too;
// when true (method-only class hosts such as npm scoped packages), valid %2f
// inside a non-traversal segment is preserved in wireRawPath.
func CanonicalizePath(rawPath, escapedPath string, allowEncodedSlash bool) (canonPath, wireRawPath string, err error) {
	if rawPath == "" {
		rawPath = "/"
	}
	if escapedPath == "" {
		escapedPath = rawPath
	}
	if !strings.HasPrefix(rawPath, "/") || !strings.HasPrefix(escapedPath, "/") {
		return "", "", fmt.Errorf("%w: request path must start with '/'. Try: send a canonical origin-form path", ErrRequestSmuggling)
	}
	if hasCTL(rawPath) || hasCTL(escapedPath) {
		return "", "", fmt.Errorf("%w: control character in request path. Try: send a clean URL path", ErrRequestSmuggling)
	}
	if strings.Contains(rawPath, "\\") || strings.Contains(escapedPath, "\\") {
		return "", "", fmt.Errorf("%w: backslash in request path. Try: use '/' separators", ErrRequestSmuggling)
	}
	if err := checkPercentEncoding(escapedPath, allowEncodedSlash); err != nil {
		return "", "", err
	}
	if strings.Contains(rawPath, "//") || strings.Contains(escapedPath, "//") {
		return "", "", fmt.Errorf("%w: non-canonical double slash in path. Try: send a normalized path", ErrRequestSmuggling)
	}
	for seg := range strings.SplitSeq(rawPath, "/") {
		if seg == "." || seg == ".." {
			return "", "", fmt.Errorf("%w: path traversal segment %q is refused. Try: send a canonical path", ErrRequestSmuggling, seg)
		}
	}
	cleaned := path.Clean(rawPath)
	if strings.HasSuffix(rawPath, "/") && cleaned != "/" {
		cleaned += "/"
	}
	if cleaned != rawPath {
		return "", "", fmt.Errorf("%w: non-canonical path %q. Try: send a canonical path", ErrRequestSmuggling, rawPath)
	}
	if allowEncodedSlash && strings.Contains(strings.ToLower(escapedPath), "%2f") {
		lowerEsc := strings.ToLower(escapedPath)
		if strings.HasPrefix(lowerEsc, "/%2f") || strings.Contains(lowerEsc, "%2f%2f") ||
			strings.Contains(lowerEsc, "%2f.") || strings.Contains(lowerEsc, ".%2f") {
			return "", "", fmt.Errorf("%w: ambiguous encoded slash in path. Try: send a canonical path", ErrRequestSmuggling)
		}
		return cleaned, escapedPath, nil
	}
	return cleaned, "", nil
}

func checkPercentEncoding(escaped string, allowEncodedSlash bool) error {
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '%' {
			continue
		}
		if i+2 >= len(escaped) {
			return fmt.Errorf("%w: truncated percent-escape in path. Try: send valid URL encoding", ErrRequestSmuggling)
		}
		hi, ok1 := hexNibble(escaped[i+1])
		lo, ok2 := hexNibble(escaped[i+2])
		if !ok1 || !ok2 {
			return fmt.Errorf("%w: invalid percent-escape in path. Try: send valid URL encoding", ErrRequestSmuggling)
		}
		b := (hi << 4) | lo
		switch {
		case b < 0x20 || b == 0x7f:
			return fmt.Errorf("%w: percent-encoded control character in path. Try: send a clean URL path", ErrRequestSmuggling)
		case b == '\\':
			return fmt.Errorf("%w: percent-encoded backslash in path. Try: use '/' separators", ErrRequestSmuggling)
		case b == '.':
			return fmt.Errorf("%w: percent-encoded dot in path. Try: send a canonical path", ErrRequestSmuggling)
		case b == '/' && !allowEncodedSlash:
			return fmt.Errorf("%w: percent-encoded slash in path. Try: send a canonical path", ErrRequestSmuggling)
		}
		i += 2
	}
	return nil
}

// CanonicalizeRequest validates an HTTP/1.1 or HTTP/2 request against its
// expected target (SNI / CONNECT host and port), query size limit, framing
// ambiguity and canonical path rules (spec 7.2).
func CanonicalizeRequest(req *http.Request, expectedHost string, expectedPort int, expectedScheme string, allowEncodedSlash bool) (CanonRequest, error) {
	if req == nil || req.URL == nil {
		return CanonRequest{}, fmt.Errorf("%w: nil HTTP request. Try: send a valid HTTP request", ErrRequestSmuggling)
	}
	method := strings.TrimSpace(req.Method)
	if !isHTTPToken(method) {
		return CanonRequest{}, fmt.Errorf("%w: invalid HTTP method %q. Try: send a standard HTTP method", ErrRequestSmuggling, req.Method)
	}
	if strings.EqualFold(method, http.MethodConnect) {
		return CanonRequest{}, fmt.Errorf("%w: nested CONNECT inside an intercepted tunnel is refused. Try: open a separate CONNECT tunnel", ErrRequestSmuggling)
	}
	if req.URL.User != nil {
		return CanonRequest{}, fmt.Errorf("%w: userinfo in request URL is refused. Try: send credentials in headers instead", ErrRequestSmuggling)
	}
	if len(req.URL.RawQuery) > MaxQueryBytes {
		return CanonRequest{}, fmt.Errorf("%w: query length %d exceeds %d bytes. Try: shorten the query string", ErrQueryTooLong, len(req.URL.RawQuery), MaxQueryBytes)
	}
	if hasCTL(req.URL.RawQuery) {
		return CanonRequest{}, fmt.Errorf("%w: control character in query string. Try: percent-encode query values", ErrRequestSmuggling)
	}

	if err := checkRequestFraming(req); err != nil {
		return CanonRequest{}, err
	}

	// Verify Host / HTTP/2 :authority matches expectedHost:expectedPort.
	if strings.TrimSpace(req.Host) == "" {
		return CanonRequest{}, fmt.Errorf("%w: missing Host or :authority header. Try: send a Host header matching %s", ErrHostMismatch, expectedHost)
	}
	hdrHost, hdrPort, err := parseURLHostPort(req.Host, expectedScheme)
	if err != nil || hdrHost != expectedHost || hdrPort != expectedPort {
		return CanonRequest{}, fmt.Errorf("%w: Host/:authority %q does not match %s:%d. Try: send a Host header matching SNI", ErrHostMismatch, req.Host, expectedHost, expectedPort)
	}

	if err := checkAbsoluteTarget(req, expectedHost, expectedPort, expectedScheme); err != nil {
		return CanonRequest{}, err
	}

	canonPath, wireRawPath, err := CanonicalizePath(req.URL.Path, req.URL.EscapedPath(), allowEncodedSlash)
	if err != nil {
		return CanonRequest{}, err
	}

	canonHost := formatHostPort(expectedHost, expectedPort)
	if expectedScheme == "http" {
		if expectedPort != 80 {
			canonHost = net.JoinHostPort(expectedHost, strconv.Itoa(expectedPort))
		} else {
			canonHost = expectedHost
		}
	}
	u := &url.URL{
		Scheme:   expectedScheme,
		Host:     canonHost,
		Path:     canonPath,
		RawPath:  wireRawPath,
		RawQuery: req.URL.RawQuery,
	}
	return CanonRequest{
		Method:      strings.ToUpper(method),
		Host:        expectedHost,
		Port:        expectedPort,
		Scheme:      expectedScheme,
		Path:        canonPath,
		WireRawPath: wireRawPath,
		RawQuery:    req.URL.RawQuery,
		URL:         u,
	}, nil
}

func checkRequestFraming(req *http.Request) error {
	if len(req.Header.Values("Host")) > 0 {
		return fmt.Errorf("%w: duplicate Host header. Try: send exactly one Host header", ErrRequestSmuggling)
	}
	clVals := req.Header.Values("Content-Length")
	if len(clVals) > 1 {
		return fmt.Errorf("%w: multiple Content-Length headers. Try: send at most one Content-Length header", ErrRequestSmuggling)
	}
	if len(clVals) == 1 {
		if _, err := parseStrictContentLength(strings.TrimSpace(clVals[0])); err != nil {
			return fmt.Errorf("%w: %w. Try: send a valid Content-Length", ErrRequestSmuggling, err)
		}
	}
	hasTE := len(req.TransferEncoding) > 0 || len(req.Header.Values("Transfer-Encoding")) > 0
	if hasTE && len(clVals) > 0 {
		return fmt.Errorf("%w: request carries both Content-Length and Transfer-Encoding. Try: send only one framing header", ErrRequestSmuggling)
	}
	if req.ProtoMajor == 2 && hasTE {
		return fmt.Errorf("%w: Transfer-Encoding is forbidden in HTTP/2. Try: omit Transfer-Encoding", ErrRequestSmuggling)
	}
	if len(req.TransferEncoding) > 1 || (len(req.TransferEncoding) == 1 && !strings.EqualFold(req.TransferEncoding[0], "chunked")) {
		return fmt.Errorf("%w: unsupported Transfer-Encoding %v. Try: use chunked or Content-Length", ErrRequestSmuggling, req.TransferEncoding)
	}
	return nil
}

func checkAbsoluteTarget(req *http.Request, expectedHost string, expectedPort int, expectedScheme string) error {
	if req.URL.Scheme != "" && !strings.EqualFold(req.URL.Scheme, expectedScheme) {
		return fmt.Errorf("%w: request URL scheme %q does not match %q. Try: use %s", ErrHostMismatch, req.URL.Scheme, expectedScheme, expectedScheme)
	}
	if req.URL.Host != "" {
		uHost, uPort, err := parseURLHostPort(req.URL.Host, expectedScheme)
		if err != nil || uHost != expectedHost || uPort != expectedPort {
			return fmt.Errorf("%w: request URL host %q does not match %s:%d. Try: send a target matching SNI", ErrHostMismatch, req.URL.Host, expectedHost, expectedPort)
		}
	}
	rawURI := strings.TrimSpace(req.RequestURI)
	if strings.Contains(rawURI, "://") {
		parsed, err := url.Parse(rawURI)
		if err != nil || parsed.User != nil {
			return fmt.Errorf("%w: invalid absolute-form request target %q. Try: use an origin-form path", ErrRequestSmuggling, rawURI)
		}
		if !strings.EqualFold(parsed.Scheme, expectedScheme) {
			return fmt.Errorf("%w: absolute target scheme %q does not match %q. Try: use %s", ErrHostMismatch, parsed.Scheme, expectedScheme, expectedScheme)
		}
		uHost, uPort, err := parseURLHostPort(parsed.Host, expectedScheme)
		if err != nil || uHost != expectedHost || uPort != expectedPort {
			return fmt.Errorf("%w: absolute target %q does not match %s:%d. Try: match the TLS SNI host", ErrHostMismatch, parsed.Host, expectedHost, expectedPort)
		}
	}
	return nil
}

// DecodeBoundedBody reads body, decompressing gzip or deflate up to maxBytes
// (default MaxDecodedBodyBytes, 64 MiB). Unsupported encodings and bodies
// expanding past maxBytes are refused (spec 7.2).
func DecodeBoundedBody(body io.ReadCloser, contentEncoding string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = MaxDecodedBodyBytes
	}
	if body == nil || body == http.NoBody {
		return nil, nil
	}
	enc := strings.ToLower(strings.TrimSpace(contentEncoding))
	var r io.Reader = body
	switch enc {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("egress: invalid gzip body: %w. Try: send valid gzip or uncompressed content", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	case "deflate":
		fl := flate.NewReader(body)
		defer func() { _ = fl.Close() }()
		r = fl
	default:
		return nil, fmt.Errorf("%w: %q is not supported. Try: use identity, gzip or deflate", ErrUnsupportedEncoding, contentEncoding)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("egress: read body: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: decoded body exceeds %d bytes. Try: send a smaller body", ErrBodyTooLarge, maxBytes)
	}
	return data, nil
}

// WrapDecodedResponse decodes a gzip or deflate HTTP response body in place up
// to maxBytes (default MaxDecodedBodyBytes) when a body-inspecting rule runs,
// refusing unknown encodings or responses exceeding maxBytes (spec 7.2).
func WrapDecodedResponse(resp *http.Response, maxBytes int64) ([]byte, error) {
	if resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		return nil, nil
	}
	data, err := DecodeBoundedBody(resp.Body, resp.Header.Get("Content-Encoding"), maxBytes)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.Header.Del("Content-Encoding")
	resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
	return data, nil
}

func isHTTPToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !ok {
			return false
		}
	}
	return true
}

func parseStrictContentLength(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty Content-Length")
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("invalid Content-Length %q", s)
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid Content-Length %q", s)
	}
	return n, nil
}

func hasInvalidHeaderValueByte(s string) bool {
	for i := range len(s) {
		c := s[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

func hasCTL(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func hasCTLOrSpace(s string) bool {
	for i := range len(s) {
		if s[i] <= 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
