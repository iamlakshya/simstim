package guest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/memberenv"
)

const (
	proxyMaxHeaderBytes = 32 << 10
	proxyHeaderTimeout  = 15 * time.Second
	// proxyMaxConcurrent leaves guest stream headroom below
	// link.DefaultMaxGuestStreams (96) for KindDNS queries.
	proxyMaxConcurrent = 64
)

// Proxy is the guest-side loopback HTTP and CONNECT forwarder (spec 7.2,
// decision 0030). It runs inside the untrusted guest init on 127.0.0.1:3128
// and [::1]:3128, extracts the target host and port, and forwards each flow
// over a KindEgress stream on the supervisor link.
type Proxy struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	active    map[io.Closer]struct{}
	sess      *link.Session
	listeners []net.Listener
	url       string
	log       *slog.Logger
	sem       chan struct{}
	done      chan struct{}
	once      sync.Once
	wg        sync.WaitGroup
}

// StartProxy binds the loopback address in proxyURL (and [::1] on the same
// port when binding 127.0.0.1 with a fixed port) and serves proxy requests
// over sess.
func StartProxy(sess *link.Session, proxyURL string, log *slog.Logger) (*Proxy, error) {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, fmt.Errorf("invalid guest proxy URL %q", proxyURL)
	}
	h, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid guest proxy host %q: %w", u.Host, err)
	}
	var lc net.ListenConfig
	ln4, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort(h, portStr))
	if err != nil {
		return nil, fmt.Errorf("listen on guest proxy %s: %w", u.Host, err)
	}
	listeners := []net.Listener{ln4}
	boundHost, boundPort, _ := net.SplitHostPort(ln4.Addr().String())
	if h == "127.0.0.1" && portStr != "0" {
		if ln6, err6 := lc.Listen(context.Background(), "tcp6", net.JoinHostPort("::1", boundPort)); err6 == nil {
			listeners = append(listeners, ln6)
		}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{ctx: ctx, cancel: cancel, active: map[io.Closer]struct{}{},
		sess:      sess,
		listeners: listeners,
		url:       "http://" + net.JoinHostPort(boundHost, boundPort),
		log:       log,
		sem:       make(chan struct{}, proxyMaxConcurrent),
		done:      make(chan struct{}),
	}
	for _, ln := range listeners {
		p.wg.Add(1)
		go p.acceptLoop(ln)
	}
	return p, nil
}

// URL returns the bound http://host:port URL of the proxy.
func (p *Proxy) URL() string { return p.url }

// Close stops accepting connections and closes all listeners.
func (p *Proxy) Close() error {
	p.once.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		close(p.done)
		for _, ln := range p.listeners {
			_ = ln.Close()
		}
		p.mu.Lock()
		for closer := range p.active {
			if deadline, ok := closer.(interface{ SetDeadline(time.Time) error }); ok {
				_ = deadline.SetDeadline(time.Now())
			}
			_ = closer.Close()
		}
		p.mu.Unlock()
	})
	p.wg.Wait()
	return nil
}

func (p *Proxy) acceptLoop(ln net.Listener) {
	defer p.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		select {
		case p.sem <- struct{}{}:
			p.wg.Add(1)
			go func(c net.Conn) {
				defer p.wg.Done()
				defer func() { <-p.sem }()
				p.serveConn(c)
			}(conn)
		default:
			writeProxyErr(conn, http.StatusServiceUnavailable, "Service Unavailable", brand.Name+": too many concurrent connections")
			_ = conn.Close()
		}
	}
}

func (p *Proxy) serveConn(conn net.Conn) {
	if !p.track(conn) {
		conn.Close()
		return
	}
	defer p.untrack(conn)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	br := bufio.NewReaderSize(&io.LimitedReader{R: conn, N: proxyMaxHeaderBytes}, 8<<10)
	rawHdr, err := readProxyHeader(br)
	if err != nil {
		writeProxyErr(conn, http.StatusBadRequest, "Bad Request", brand.Name+": malformed proxy request")
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	tail := bufferedBytes(br)
	hostName, port, forwardHdr, err := extractProxyTarget(rawHdr)
	if err != nil {
		writeProxyErr(conn, http.StatusBadRequest, "Bad Request", brand.Name+": "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(p.ctx, proxyHeaderTimeout)
	st, err := p.sess.Open(ctx, link.KindEgress, map[string]any{
		"via":  "explicit",
		"host": hostName,
		"port": port,
	})
	cancel()
	if err != nil {
		writeProxyErr(conn, http.StatusBadGateway, "Bad Gateway", brand.Name+": egress unavailable")
		return
	}
	if !p.track(st) {
		st.Close()
		return
	}
	defer p.untrack(st)
	defer st.Close()

	if _, err := st.Write(forwardHdr); err != nil {
		writeProxyErr(conn, http.StatusBadGateway, "Bad Gateway", brand.Name+": egress unavailable")
		return
	}
	if len(tail) > 0 {
		if _, err := st.Write(tail); err != nil {
			writeProxyErr(conn, http.StatusBadGateway, "Bad Gateway", brand.Name+": egress unavailable")
			return
		}
	}
	spliceGuestFlow(st, conn)
}

func spliceGuestFlow(st *link.Stream, conn net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(st, conn)
		_ = st.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		n, err := io.Copy(conn, st)
		if n == 0 && errors.Is(err, link.ErrRefused) {
			writeProxyErr(conn, http.StatusBadGateway, "Bad Gateway", brand.Name+": egress closed")
		}
		if tc, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
}

func readProxyHeader(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for len(buf) < proxyMaxHeaderBytes {
		line, err := br.ReadSlice('\n')
		buf = append(buf, line...)
		if bytes.HasSuffix(buf, []byte("\r\n\r\n")) || bytes.HasSuffix(buf, []byte("\n\n")) {
			return buf, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read proxy header: %w", err)
		}
	}
	return nil, errors.New("HTTP header too large")
}

func bufferedBytes(br *bufio.Reader) []byte {
	n := br.Buffered()
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	_, _ = io.ReadFull(br, out)
	return out
}

func extractProxyTarget(rawHdr []byte) (hostName string, port int, outHdr []byte, err error) {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(rawHdr)))
	if err != nil {
		return "", 0, nil, errors.New("invalid HTTP request")
	}
	outHdr = rawHdr
	if req.Method == http.MethodConnect {
		h, p, err := splitProxyHostPort(req.RequestURI, 443)
		return h, p, outHdr, err
	}
	if strings.HasPrefix(strings.ToLower(req.RequestURI), "http://") {
		u, err := url.Parse(req.RequestURI)
		if err != nil || u.Host == "" {
			return "", 0, nil, errors.New("invalid proxy URL")
		}
		h, p, err := splitProxyHostPort(u.Host, 80)
		return h, p, outHdr, err
	}
	if strings.HasPrefix(req.RequestURI, "/") && req.Host != "" {
		h, p, err := splitProxyHostPort(req.Host, 80)
		if err != nil {
			return "", 0, nil, err
		}
		outHdr = rewriteOriginFormToAbsolute(rawHdr, req.Method, req.Host, req.RequestURI)
		return h, p, outHdr, nil
	}
	return "", 0, nil, errors.New("unsupported proxy request target")
}

func rewriteOriginFormToAbsolute(rawHdr []byte, method, hostHdr, reqURI string) []byte {
	nl := bytes.IndexByte(rawHdr, '\n')
	if nl < 0 {
		return rawHdr
	}
	firstLine := fmt.Sprintf("%s http://%s%s HTTP/1.1\r\n", method, hostHdr, reqURI)
	var out bytes.Buffer
	out.WriteString(firstLine)
	out.Write(rawHdr[nl+1:])
	return out.Bytes()
}

func splitProxyHostPort(s string, defPort int) (string, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, errors.New("empty proxy target")
	}
	var h, portStr string
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", 0, errors.New("unclosed IPv6 bracket")
		}
		h = s[1:end]
		rest := s[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", 0, errors.New("invalid host:port")
			}
			portStr = rest[1:]
		}
	} else if idx := strings.LastIndexByte(s, ':'); idx >= 0 && !strings.Contains(s[:idx], ":") {
		h = s[:idx]
		portStr = s[idx+1:]
	} else {
		h = s
	}
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if h == "" {
		return "", 0, errors.New("empty target host")
	}
	port := defPort
	if portStr != "" {
		n, err := strconv.Atoi(portStr)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, errors.New("invalid target port")
		}
		port = n
	}
	return h, port, nil
}

func writeProxyErr(w io.Writer, code int, status, msg string) {
	body := msg + "\n"
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, status, len(body), body)
}

func rewriteProxyEnv(env []string, actualURL string) []string {
	return memberenv.Proxy(env, host.DefaultGuestProxy, actualURL)
}

func (p *Proxy) track(closer io.Closer) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return false
	default:
	}
	p.active[closer] = struct{}{}
	return true
}
func (p *Proxy) untrack(closer io.Closer) {
	p.mu.Lock()
	delete(p.active, closer)
	p.mu.Unlock()
}
