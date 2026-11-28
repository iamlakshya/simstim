package egress

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// http1Framing describes the validated body framing of one HTTP/1.x request.
type http1Framing struct {
	contentLength int64
	chunked       bool
}

type http1HeadState struct {
	hostCount int
	clCount   int
	teCount   int
	clValue   int64
	hasChunk  bool
}

// ValidateRawHTTP1Head checks raw HTTP/1.x request head bytes (up to and
// including the blank "\r\n\r\n" terminator) before net/http parses them. It
// refuses bare LF/CR, obsolete line folding, whitespace before ':', duplicate
// Host headers, both Content-Length and Transfer-Encoding, multiple
// Content-Length headers, non-chunked or duplicate Transfer-Encoding, and
// CONNECT inside an intercepted tunnel.
func ValidateRawHTTP1Head(head []byte) (http1Framing, error) {
	if len(head) == 0 {
		return http1Framing{}, fmt.Errorf("%w: empty HTTP request head. Try: send a valid HTTP/1.1 request", ErrRequestSmuggling)
	}
	if len(head) > MaxHTTP1HeadBytes {
		return http1Framing{}, fmt.Errorf("%w: HTTP request headers exceed 64 KiB limit. Try: reduce header size", ErrRequestSmuggling)
	}
	if !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		return http1Framing{}, fmt.Errorf("%w: HTTP request head must end with CRLF CRLF. Try: use standard CRLF line endings", ErrRequestSmuggling)
	}
	lines := bytes.Split(head[:len(head)-4], []byte("\r\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		return http1Framing{}, fmt.Errorf("%w: missing HTTP request line. Try: send a valid HTTP/1.1 request", ErrRequestSmuggling)
	}

	protoMajor, protoMinor, err := validateHTTP1RequestLine(lines[0])
	if err != nil {
		return http1Framing{}, err
	}

	var st http1HeadState
	for _, line := range lines[1:] {
		if err := validateHTTP1HeaderLine(line, &st); err != nil {
			return http1Framing{}, err
		}
	}
	if protoMajor == 1 && protoMinor >= 1 && st.hostCount == 0 {
		return http1Framing{}, fmt.Errorf("%w: missing Host header in HTTP/1.1 request. Try: include a Host header matching SNI", ErrRequestSmuggling)
	}
	if st.clCount > 0 && st.teCount > 0 {
		return http1Framing{}, fmt.Errorf("%w: request carries both Content-Length and Transfer-Encoding. Try: send only one framing header", ErrRequestSmuggling)
	}
	return http1Framing{
		contentLength: st.clValue,
		chunked:       st.hasChunk,
	}, nil
}

func validateHTTP1HeaderLine(line []byte, st *http1HeadState) error {
	if len(line) == 0 {
		return fmt.Errorf("%w: unexpected empty header line. Try: send well-formed HTTP headers", ErrRequestSmuggling)
	}
	if bytes.IndexByte(line, '\r') >= 0 || bytes.IndexByte(line, '\n') >= 0 {
		return fmt.Errorf("%w: bare CR or LF inside header line. Try: use CRLF line endings only", ErrRequestSmuggling)
	}
	if line[0] == ' ' || line[0] == '\t' {
		return fmt.Errorf("%w: obsolete header line folding is refused. Try: send single-line headers", ErrRequestSmuggling)
	}
	colon := bytes.IndexByte(line, ':')
	if colon <= 0 {
		return fmt.Errorf("%w: malformed HTTP header line. Try: use Name: Value syntax", ErrRequestSmuggling)
	}
	name := string(line[:colon])
	if !isHTTPToken(name) {
		return fmt.Errorf("%w: invalid HTTP header name %q. Try: remove spaces or control characters from header names", ErrRequestSmuggling, name)
	}
	val := string(line[colon+1:])
	if hasInvalidHeaderValueByte(val) {
		return fmt.Errorf("%w: invalid control character in header %q. Try: send valid RFC 9110 header values", ErrRequestSmuggling, name)
	}
	return recordHTTP1Header(name, strings.Trim(val, " \t"), st)
}

func recordHTTP1Header(name, trimmed string, st *http1HeadState) error {
	switch {
	case strings.EqualFold(name, "Host"):
		st.hostCount++
		if st.hostCount > 1 {
			return fmt.Errorf("%w: duplicate Host header. Try: send exactly one Host header", ErrRequestSmuggling)
		}
	case strings.EqualFold(name, "Content-Length"):
		st.clCount++
		if st.clCount > 1 {
			return fmt.Errorf("%w: multiple Content-Length headers. Try: send at most one Content-Length header", ErrRequestSmuggling)
		}
		n, err := parseStrictContentLength(trimmed)
		if err != nil {
			return fmt.Errorf("%w: %w. Try: send a single non-negative integer Content-Length", ErrRequestSmuggling, err)
		}
		st.clValue = n
	case strings.EqualFold(name, "Transfer-Encoding"):
		st.teCount++
		if st.teCount > 1 {
			return fmt.Errorf("%w: multiple Transfer-Encoding headers. Try: use a single Transfer-Encoding: chunked header", ErrRequestSmuggling)
		}
		if !strings.EqualFold(trimmed, "chunked") {
			return fmt.Errorf("%w: unsupported Transfer-Encoding %q. Try: use chunked or Content-Length", ErrRequestSmuggling, trimmed)
		}
		st.hasChunk = true
	}
	return nil
}

func validateHTTP1RequestLine(line []byte) (int, int, error) {
	if bytes.IndexByte(line, '\r') >= 0 || bytes.IndexByte(line, '\n') >= 0 {
		return 0, 0, fmt.Errorf("%w: bare CR or LF in request line. Try: use CRLF line endings", ErrRequestSmuggling)
	}
	parts := bytes.Split(line, []byte(" "))
	if len(parts) != 3 {
		return 0, 0, fmt.Errorf("%w: malformed HTTP request line. Try: use METHOD TARGET HTTP/1.1", ErrRequestSmuggling)
	}
	method, target, proto := string(parts[0]), string(parts[1]), string(parts[2])
	if !isHTTPToken(method) {
		return 0, 0, fmt.Errorf("%w: invalid HTTP method %q. Try: send a standard HTTP method", ErrRequestSmuggling, method)
	}
	if strings.EqualFold(method, http.MethodConnect) {
		return 0, 0, fmt.Errorf("%w: nested CONNECT inside an intercepted tunnel is refused. Try: open a separate CONNECT tunnel", ErrRequestSmuggling)
	}
	if target == "" || hasCTLOrSpace(target) {
		return 0, 0, fmt.Errorf("%w: invalid request target. Try: send a valid origin-form or absolute-form URL", ErrRequestSmuggling)
	}
	switch proto {
	case "HTTP/1.1":
		return 1, 1, nil
	case "HTTP/1.0":
		return 1, 0, nil
	default:
		return 0, 0, fmt.Errorf("%w: unsupported HTTP version %q. Try: use HTTP/1.1 or HTTP/2", ErrRequestSmuggling, proto)
	}
}

// validatingHTTP1Conn wraps a decrypted TLS connection before net/http.Server
// parses HTTP/1.x messages. It validates each request head with
// ValidateRawHTTP1Head and never crosses message boundaries in Read so
// keep-alive and pipelined smuggling attempts are refused deterministically.
// Passthrough mode is only enabled after the server actually hijacks the
// connection (for example after an upstream 101 Switching Protocols upgrade),
// never from an unconfirmed client Upgrade header.
type validatingHTTP1Conn struct {
	net.Conn
	br          *bufio.Reader
	onReject    func(err error)
	passthrough atomic.Bool

	state   int
	pending []byte
	remBody int64
	chunked chunkState
}

const (
	h1StateHead = iota
	h1StateBodyFixed
	h1StateBodyChunked
)

type chunkState struct {
	stage int // 0=size line, 1=data+CRLF, 2=trailers
	rem   int64
}

func newValidatingHTTP1Conn(conn net.Conn, onReject func(error)) *validatingHTTP1Conn {
	return &validatingHTTP1Conn{
		Conn:     conn,
		br:       bufio.NewReaderSize(conn, 16<<10),
		onReject: onReject,
		state:    h1StateHead,
	}
}

// enablePassthrough switches the connection to raw forwarding once the HTTP
// server has hijacked it after a confirmed protocol upgrade (101).
func (c *validatingHTTP1Conn) enablePassthrough() {
	c.passthrough.Store(true)
}

func (c *validatingHTTP1Conn) Read(p []byte) (int, error) {
	for {
		if len(c.pending) > 0 {
			n := copy(p, c.pending)
			c.pending = c.pending[n:]
			return n, nil
		}
		if c.passthrough.Load() {
			return c.br.Read(p) //nolint:wrapcheck // net.Conn.Read must preserve io.EOF and net.Error directly
		}
		switch c.state {
		case h1StateHead:
			if err := c.stepHead(); err != nil {
				return 0, err
			}
		case h1StateBodyFixed:
			if c.remBody <= 0 {
				c.state = h1StateHead
				continue
			}
			return c.readFixedBody(p)
		case h1StateBodyChunked:
			if err := c.stepChunked(); err != nil {
				if errors.Is(err, ErrRequestSmuggling) {
					c.reject(err)
					return 0, io.EOF
				}
				return 0, err
			}
		}
	}
}

func (c *validatingHTTP1Conn) stepHead() error {
	head, err := readRawHTTP1Head(c.br, MaxHTTP1HeadBytes)
	if err != nil {
		if errors.Is(err, ErrRequestSmuggling) {
			c.reject(err)
			return io.EOF
		}
		return err
	}
	framing, err := ValidateRawHTTP1Head(head)
	if err != nil {
		c.reject(err)
		return io.EOF
	}
	c.pending = head
	switch {
	case framing.chunked:
		c.state = h1StateBodyChunked
		c.chunked = chunkState{stage: 0}
	case framing.contentLength > 0:
		c.state = h1StateBodyFixed
		c.remBody = framing.contentLength
	default:
		c.state = h1StateHead
	}
	return nil
}

func (c *validatingHTTP1Conn) readFixedBody(p []byte) (int, error) {
	want := len(p)
	if int64(want) > c.remBody {
		want = int(c.remBody)
	}
	n, err := c.br.Read(p[:want])
	c.remBody -= int64(n)
	if c.remBody <= 0 {
		c.state = h1StateHead
	}
	return n, err //nolint:wrapcheck // net.Conn.Read must preserve io.EOF and net.Error directly
}

func (c *validatingHTTP1Conn) stepChunked() error {
	switch c.chunked.stage {
	case 0:
		line, err := readBoundedLine(c.br, 4096)
		if err != nil {
			return err
		}
		if !bytes.HasSuffix(line, []byte("\r\n")) {
			return fmt.Errorf("%w: chunk size line missing CRLF. Try: send valid chunked encoding", ErrRequestSmuggling)
		}
		sizeToken := string(line[:len(line)-2])
		if semi := strings.IndexByte(sizeToken, ';'); semi >= 0 {
			sizeToken = sizeToken[:semi]
		}
		sizeToken = strings.TrimSpace(sizeToken)
		if sizeToken == "" {
			return fmt.Errorf("%w: empty chunk size. Try: send valid chunked encoding", ErrRequestSmuggling)
		}
		n, err := strconv.ParseInt(sizeToken, 16, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("%w: invalid chunk size %q. Try: send valid hex chunk sizes", ErrRequestSmuggling, sizeToken)
		}
		c.pending = line
		if n == 0 {
			c.chunked.stage = 2
		} else {
			c.chunked.stage = 1
			c.chunked.rem = n + 2 // chunk bytes + trailing CRLF
		}
		return nil
	case 1:
		want := int64(16 << 10)
		if c.chunked.rem < want {
			want = c.chunked.rem
		}
		buf := make([]byte, want)
		n, err := c.br.Read(buf)
		if n > 0 {
			c.pending = buf[:n]
			c.chunked.rem -= int64(n)
			if c.chunked.rem <= 0 {
				c.chunked.stage = 0
			}
			return nil
		}
		return err //nolint:wrapcheck // net.Conn.Read preserves io.EOF and net.Error directly
	default:
		line, err := readBoundedLine(c.br, 8192)
		if err != nil {
			return err
		}
		if !bytes.HasSuffix(line, []byte("\r\n")) {
			return fmt.Errorf("%w: chunk trailer missing CRLF. Try: send valid chunked encoding", ErrRequestSmuggling)
		}
		c.pending = line
		if len(line) == 2 {
			c.state = h1StateHead
		}
		return nil
	}
}

func (c *validatingHTTP1Conn) reject(err error) {
	if c.onReject != nil {
		c.onReject(err)
	}
	msg := err.Error() + "\n"
	_, _ = fmt.Fprintf(c.Conn, "HTTP/1.1 400 Bad Request\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
	_ = c.Close()
}

func readRawHTTP1Head(br *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf bytes.Buffer
	for {
		line, err := readBoundedLine(br, maxBytes-buf.Len())
		if err != nil {
			if errors.Is(err, io.EOF) && buf.Len() == 0 && len(line) == 0 {
				return nil, io.EOF
			}
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%w: truncated HTTP request head. Try: send a complete HTTP/1.1 request", ErrRequestSmuggling)
			}
			return nil, err
		}
		buf.Write(line)
		if buf.Len() > maxBytes {
			return nil, fmt.Errorf("%w: HTTP request headers exceed %d bytes. Try: reduce header size", ErrRequestSmuggling, maxBytes)
		}
		if bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n")) {
			return buf.Bytes(), nil
		}
	}
}

func readBoundedLine(br *bufio.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: HTTP header line exceeds limit. Try: reduce header size", ErrRequestSmuggling)
	}
	var out []byte
	for {
		frag, err := br.ReadSlice('\n')
		out = append(out, frag...)
		if len(out) > maxBytes {
			return nil, fmt.Errorf("%w: HTTP header line exceeds limit. Try: reduce header size", ErrRequestSmuggling)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return out, err //nolint:wrapcheck // preserves io.EOF and net.Error directly for caller
	}
}
