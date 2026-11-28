package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type interceptTestResolver struct {
	addr netip.Addr
}

func (r interceptTestResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.addr}, nil
}

type interceptHarness struct {
	upstream     *httptest.Server
	proxySrv     *httptest.Server
	egressSrv    *Server
	engine       *Engine
	upstreamHits atomic.Int64

	auditMu sync.Mutex
	audits  []AuditEvent
}

func newInterceptHarness(t *testing.T, cfg Config, trustUpstream bool) *interceptHarness {
	t.Helper()
	h := &interceptHarness{}
	h.upstream = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.upstreamHits.Add(1)
		w.Header().Set("X-Upstream-Proto", r.Proto)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "%s %s", r.Method, r.URL.RequestURI())
	}))
	h.upstream.EnableHTTP2 = true
	h.upstream.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	}
	// Issue an upstream cert valid for our test domain names so real TLS
	// verification passes without disabling InsecureSkipVerify.
	upCA, err := NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA upstream: %v", err)
	}
	t.Cleanup(upCA.Close)
	h.upstream.TLS.GetCertificate = func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := chi.ServerName
		if name == "" {
			name = "localhost"
		}
		return upCA.LeafForHost(name)
	}
	h.upstream.StartTLS()
	t.Cleanup(h.upstream.Close)

	upAddr := h.upstream.Listener.Addr().(*net.TCPAddr)
	upPort := upAddr.Port

	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	h.engine = eng

	addrs, err := NewAddressPolicy()
	if err != nil {
		t.Fatalf("NewAddressPolicy: %v", err)
	}
	dialer := NewSafeDialer(addrs, eng)
	// Resolve every host to public TEST-NET-2 IP 198.51.100.10 and dial our
	// local TLS upstream on its ephemeral port.
	dialer.Resolver = interceptTestResolver{addr: netip.MustParseAddr("198.51.100.10")}
	dialer.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", upPort))
	}

	var upTLS *tls.Config
	if trustUpstream {
		upTLS = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    upCA.CertPool(),
		}
	} else {
		// Trust an unrelated CA so upstream verification fails closed.
		otherCA, err := NewSessionCA()
		if err != nil {
			t.Fatalf("NewSessionCA untrusted: %v", err)
		}
		t.Cleanup(otherCA.Close)
		upTLS = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    otherCA.CertPool(),
		}
	}

	egSrv, err := NewServer(ServerOptions{
		Engine:      eng,
		Addrs:       addrs,
		Dialer:      dialer,
		UpstreamTLS: upTLS,
		Audit: func(ev AuditEvent) {
			h.auditMu.Lock()
			h.audits = append(h.audits, ev)
			h.auditMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(egSrv.Close)
	h.egressSrv = egSrv

	h.proxySrv = httptest.NewServer(egSrv)
	t.Cleanup(h.proxySrv.Close)
	return h
}

func (h *interceptHarness) client(t *testing.T, forceH2 bool) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse(h.proxySrv.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    h.egressSrv.CA.CertPool(),
	}
	if forceH2 {
		tlsCfg.NextProtos = []string{"h2", "http/1.1"}
	} else {
		tlsCfg.NextProtos = []string{"http/1.1"}
	}
	tr := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		TLSClientConfig:   tlsCfg,
		ForceAttemptHTTP2: forceH2,
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func (h *interceptHarness) events() []AuditEvent {
	h.auditMu.Lock()
	defer h.auditMu.Unlock()
	return append([]AuditEvent(nil), h.audits...)
}

func TestInterceptRegistryReadOnly(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/p22",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
	}, true)

	cl := h.client(t, false)

	// GET & HEAD pass (including scoped npm package with %2f).
	for _, tc := range []struct {
		method string
		rawURL string
	}{
		{"GET", "https://registry.npmjs.org/lodash?secret=ignored"},
		{"HEAD", "https://registry.npmjs.org/lodash"},
		{"GET", "https://registry.npmjs.org/@types%2fnode"},
		{"GET", "https://pypi.org/simple/requests/"},
		{"GET", "https://proxy.golang.org/golang.org/x/net/@v/list"},
		{"GET", "https://crates.io/api/v1/crates/serde"},
	} {
		req, err := http.NewRequest(tc.method, tc.rawURL, nil)
		if err != nil {
			t.Fatalf("NewRequest(%s %s): %v", tc.method, tc.rawURL, err)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("Do(%s %s): %v", tc.method, tc.rawURL, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s = %d (%s), want 200", tc.method, tc.rawURL, resp.StatusCode, body)
		}
	}

	hitsBefore := h.upstreamHits.Load()

	// Writes (npm publish, unpublish, token mint, PyPI upload, cargo publish) get 403.
	for _, tc := range []struct {
		method string
		rawURL string
	}{
		{"PUT", "https://registry.npmjs.org/my-pkg"},
		{"POST", "https://registry.npmjs.org/-/npm/v1/tokens"},
		{"DELETE", "https://registry.npmjs.org/my-pkg/-rev/1"},
		{"POST", "https://pypi.org/legacy/"},
		{"PUT", "https://crates.io/api/v1/crates/new"},
	} {
		req, _ := http.NewRequest(tc.method, tc.rawURL, strings.NewReader(`{"name":"my-pkg"}`))
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("Do(%s %s): %v", tc.method, tc.rawURL, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s = %d (%s), want 403", tc.method, tc.rawURL, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "Try:") {
			t.Fatalf("%s %s 403 body missing Try hint: %q", tc.method, tc.rawURL, body)
		}
	}

	if got := h.upstreamHits.Load(); got != hitsBefore {
		t.Fatalf("refused registry writes reached upstream (%d -> %d)", hitsBefore, got)
	}

	// Verify audit lines record mode:"intercept" and never include query strings.
	var sawAllow, sawDeny bool
	for _, ev := range h.events() {
		if strings.Contains(ev.Path, "secret=ignored") || strings.Contains(ev.Path, "?") {
			t.Fatalf("audit path leaked query string: %+v", ev)
		}
		if ev.Mode == string(ModeIntercept) && ev.Decision == string(ActionAllow) && ev.Host == "registry.npmjs.org" {
			sawAllow = true
		}
		if ev.Mode == string(ModeIntercept) && ev.Decision == string(ActionDeny) && ev.Rule == "class:registry" {
			sawDeny = true
		}
	}
	if !sawAllow || !sawDeny {
		t.Fatalf("missing expected intercept audit events: sawAllow=%v sawDeny=%v", sawAllow, sawDeny)
	}
}

func TestInterceptObjectStoreReadOnlyAndExplicitGrant(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/s3",
		Sandbox: SandboxConfig{Preset: PresetLocked},
		Network: NetworkConfig{Allow: []string{"mybucket.s3.amazonaws.com"}},
	}, true)
	cl := h.client(t, false)

	resp, err := cl.Get("https://mybucket.s3.amazonaws.com/data/item.json")
	if err != nil {
		t.Fatalf("GET s3: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET s3 status = %d, want 200", resp.StatusCode)
	}

	putReq, _ := http.NewRequest(http.MethodPut, "https://mybucket.s3.amazonaws.com/uploads/a.bin", strings.NewReader("data"))
	resp, err = cl.Do(putReq)
	if err != nil {
		t.Fatalf("PUT s3: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT s3 status = %d, want 403", resp.StatusCode)
	}

	// Explicit method+path live grant opens uploads/* while keeping other paths blocked.
	if err := h.engine.Allow("PUT mybucket.s3.amazonaws.com/uploads/*"); err != nil {
		t.Fatalf("Allow scoped PUT: %v", err)
	}
	putOK, _ := http.NewRequest(http.MethodPut, "https://mybucket.s3.amazonaws.com/uploads/a.bin", strings.NewReader("data"))
	resp, err = cl.Do(putOK)
	if err != nil {
		t.Fatalf("PUT s3 granted: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT s3 granted status = %d, want 200", resp.StatusCode)
	}

	putDenied, _ := http.NewRequest(http.MethodPut, "https://mybucket.s3.amazonaws.com/other/a.bin", strings.NewReader("data"))
	resp, err = cl.Do(putDenied)
	if err != nil {
		t.Fatalf("PUT s3 other: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT s3 other status = %d, want 403", resp.StatusCode)
	}
}

func TestInterceptMethodPathRulesAndDenyWins(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/rules",
		Sandbox: SandboxConfig{Preset: PresetLocked},
		Network: NetworkConfig{
			Allow: []string{"GET api.example.com/v1/*", "POST api.example.com/v1/items"},
			Deny:  []string{"GET api.example.com/v1/secret"},
		},
		Lock: LockConfig{
			Deny: []string{"DELETE api.example.com/**"},
		},
	}, true)
	cl := h.client(t, false)

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{"GET", "/v1/users", http.StatusOK},
		{"POST", "/v1/items", http.StatusOK},
		{"POST", "/v1/users", http.StatusForbidden},
		{"GET", "/v1/secret", http.StatusForbidden},
		{"GET", "/v2/users", http.StatusForbidden},
		{"DELETE", "/v1/items", http.StatusForbidden},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, "https://api.example.com"+tc.path, nil)
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}

	// Lock cannot be overridden by live Allow.
	if err := h.engine.Allow("DELETE api.example.com/v1/items"); !errors.Is(err, ErrPolicyLocked) {
		t.Fatalf("Allow locked DELETE err = %v, want ErrPolicyLocked", err)
	}
}

func TestInterceptHTTP2OverALPN(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/h2",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
	}, true)

	cl := h.client(t, true)
	resp, err := cl.Get("https://registry.npmjs.org/lodash")
	if err != nil {
		t.Fatalf("H2 GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("negotiated HTTP version = %s, want HTTP/2", resp.Proto)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("H2 GET status = %d, want 200", resp.StatusCode)
	}

	// HTTP/2 PUT (publish) is refused with 403.
	putReq, _ := http.NewRequest(http.MethodPut, "https://registry.npmjs.org/pkg", strings.NewReader("{}"))
	resp, err = cl.Do(putReq)
	if err != nil {
		t.Fatalf("H2 PUT: %v", err)
	}
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("H2 PUT = %s %d, want HTTP/2 403", resp.Proto, resp.StatusCode)
	}

	// HTTP/2 request whose :authority differs from SNI is refused with 400.
	tlsConn := dialInterceptTLS(t, h.proxySrv.Listener.Addr().String(), "registry.npmjs.org:443", "registry.npmjs.org", h.egressSrv.CA.CertPool(), []string{"h2"})
	defer tlsConn.Close()
	var h2Protos http.Protocols
	h2Protos.SetHTTP2(true)
	h2Tr := &http.Transport{
		Protocols: &h2Protos,
		DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			return tlsConn, nil
		},
	}
	defer h2Tr.CloseIdleConnections()
	badReq, _ := http.NewRequest(http.MethodGet, "https://evil.example/steal", nil)
	badResp, err := h2Tr.RoundTrip(badReq)
	if err != nil {
		t.Fatalf("H2 bad authority RoundTrip: %v", err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("H2 :authority != SNI status = %d, want 400", badResp.StatusCode)
	}
}

func TestInterceptWireSmugglingAndUpstreamTLS(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/smuggle",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
	}, true)

	proxyAddr := h.proxySrv.Listener.Addr().String()
	pool := h.egressSrv.CA.CertPool()

	cases := []struct {
		name       string
		rawReq     string
		wantStatus int
	}{
		{
			name:       "CL and TE chunked",
			rawReq:     "POST /pkg HTTP/1.1\r\nHost: registry.npmjs.org\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "duplicate Host",
			rawReq:     "GET /pkg HTTP/1.1\r\nHost: registry.npmjs.org\r\nHost: evil.example\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Host differs from SNI",
			rawReq:     "GET /pkg HTTP/1.1\r\nHost: evil.example\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "absolute target differs from SNI",
			rawReq:     "GET https://evil.example/pkg HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "query exceeds 8 KiB",
			rawReq:     "GET /pkg?" + strings.Repeat("a", MaxQueryBytes+1) + " HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n",
			wantStatus: http.StatusRequestURITooLong,
		},
		{
			name:       "nested CONNECT",
			rawReq:     "CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tlsConn := dialInterceptTLS(t, proxyAddr, "registry.npmjs.org:443", "registry.npmjs.org", pool, []string{"http/1.1"})
			defer tlsConn.Close()
			if _, err := io.WriteString(tlsConn, tc.rawReq); err != nil {
				t.Fatalf("write raw req: %v", err)
			}
			_ = tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			resp, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
			if err != nil {
				t.Fatalf("ReadResponse: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
	if hits := h.upstreamHits.Load(); hits != 0 {
		t.Fatalf("smuggling attempts reached upstream (%d hits)", hits)
	}

	// Untrusted upstream certificate must fail closed (502), never skip verification.
	hBadUp := newInterceptHarness(t, Config{
		Session: "devbox/bad-up",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
	}, false)
	resp, err := hBadUp.client(t, false).Get("https://registry.npmjs.org/lodash")
	if err != nil {
		t.Fatalf("GET untrusted upstream: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("untrusted upstream status = %d, want 502", resp.StatusCode)
	}
}

func TestSpliceOnlyListNeverIntercepted(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/splice",
		Sandbox: SandboxConfig{Preset: PresetLocked},
		Network: NetworkConfig{
			Allow: []string{"mtls.googleapis.com"},
			Deny:  []string{"DELETE mtls.googleapis.com/*"},
		},
	}, true)

	if !h.engine.IsSpliceOnly("mtls.googleapis.com") {
		t.Fatal("mtls.googleapis.com should be on splice-only list")
	}
	if intercept, why := h.engine.ShouldIntercept("mtls.googleapis.com", 443); intercept || why != "splice_only" {
		t.Fatalf("ShouldIntercept = (%v, %q), want (false, splice_only)", intercept, why)
	}

	// Connecting via CONNECT must NOT trust the session CA — the upstream cert
	// is presented end-to-end (spliced), so trusting only the session CA fails.
	raw, err := net.DialTimeout("tcp", h.proxySrv.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(raw, "CONNECT mtls.googleapis.com:443 HTTP/1.1\r\nHost: mtls.googleapis.com:443\r\n\r\n")
	br := bufio.NewReader(raw)
	connResp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT mtls: err=%v", err)
	}
	_ = connResp.Body.Close()
	if connResp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT mtls: status=%d, want 200", connResp.StatusCode)
	}
	tlsConn := tls.Client(raw, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "mtls.googleapis.com",
		RootCAs:    h.egressSrv.CA.CertPool(),
	})
	if err := tlsConn.Handshake(); err == nil {
		t.Fatal("splice-only host was unexpectedly intercepted by session CA")
	}
}

func TestInterceptTransparentMode(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/transp",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
	}, true)

	c1, c2 := net.Pipe()
	defer c1.Close()
	dl := time.Now().Add(5 * time.Second)
	_ = c1.SetDeadline(dl)
	_ = c2.SetDeadline(dl)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.egressSrv.HandleTransparent(ctx, c2, "registry.npmjs.org", 443)
	}()

	tlsConn := tls.Client(c1, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "registry.npmjs.org",
		RootCAs:    h.egressSrv.CA.CertPool(),
		NextProtos: []string{"http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("transparent TLS handshake: %v", err)
	}

	_, _ = io.WriteString(tlsConn, "GET /lodash HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n")
	br := bufio.NewReader(tlsConn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("transparent GET resp: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("transparent GET = %d, want 200", resp.StatusCode)
	}

	_, _ = io.WriteString(tlsConn, "PUT /pkg HTTP/1.1\r\nHost: registry.npmjs.org\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("transparent PUT resp: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("transparent PUT = %d, want 403", resp.StatusCode)
	}
	_ = tlsConn.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("HandleTransparent did not exit within timeout")
	}
}

func TestInterceptUpgradeHeaderKeepAliveStillRefusesSmuggling(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/upgrade-smuggle",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
	}, true)

	tlsConn := dialInterceptTLS(t, h.proxySrv.Listener.Addr().String(), "registry.npmjs.org:443", "registry.npmjs.org", h.egressSrv.CA.CertPool(), []string{"http/1.1"})
	defer tlsConn.Close()
	_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))

	br := bufio.NewReader(tlsConn)
	_, err := io.WriteString(tlsConn, "GET /lodash HTTP/1.1\r\nHost: registry.npmjs.org\r\nUpgrade: foo\r\n\r\n")
	if err != nil {
		t.Fatalf("write req 1: %v", err)
	}
	resp1, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read resp 1: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("req 1 status = %d, want 200", resp1.StatusCode)
	}

	hitsBefore := h.upstreamHits.Load()
	_, err = io.WriteString(tlsConn, "POST /pkg HTTP/1.1\r\nHost: registry.npmjs.org\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n")
	if err != nil {
		t.Fatalf("write smuggled req 2: %v", err)
	}
	resp2, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read resp 2: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("smuggled req 2 after Upgrade header = %d, want 400", resp2.StatusCode)
	}
	if h.upstreamHits.Load() != hitsBefore {
		t.Fatalf("smuggled req 2 reached upstream")
	}
}

func TestInterceptTransparentNon443Port(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/transp-8443",
		Sandbox: SandboxConfig{Preset: PresetLocked},
		Network: NetworkConfig{
			Allow: []string{"GET api.example.com:8443/v1/*", "registry.npmjs.org:8443"},
		},
	}, true)

	t.Run("port-scoped HTTP rule enforced over transparent TLS on 8443", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		dl := time.Now().Add(5 * time.Second)
		_ = c1.SetDeadline(dl)
		_ = c2.SetDeadline(dl)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.egressSrv.HandleTransparent(ctx, c2, "api.example.com", 8443)
		}()

		tlsConn := tls.Client(c1, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: "api.example.com",
			RootCAs:    h.egressSrv.CA.CertPool(),
			NextProtos: []string{"http/1.1"},
		})
		if err := tlsConn.Handshake(); err != nil {
			t.Fatalf("transparent :8443 TLS handshake: %v", err)
		}

		_, _ = io.WriteString(tlsConn, "GET /v1/items HTTP/1.1\r\nHost: api.example.com:8443\r\n\r\n")
		br := bufio.NewReader(tlsConn)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("GET /v1/items: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/items = %d, want 200", resp.StatusCode)
		}

		_, _ = io.WriteString(tlsConn, "POST /v1/items HTTP/1.1\r\nHost: api.example.com:8443\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
		resp, err = http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("POST /v1/items: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("POST /v1/items = %d, want 403", resp.StatusCode)
		}
		_ = tlsConn.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("HandleTransparent :8443 did not exit")
		}
	})

	t.Run("non-TLS raw TCP on intercepted non-443 port fails closed", func(t *testing.T) {
		hitsBefore := h.upstreamHits.Load()
		c1, c2 := net.Pipe()
		defer c1.Close()
		dl := time.Now().Add(5 * time.Second)
		_ = c1.SetDeadline(dl)
		_ = c2.SetDeadline(dl)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.egressSrv.HandleTransparent(ctx, c2, "registry.npmjs.org", 8443)
		}()

		_, _ = io.WriteString(c1, "PUT /pkg HTTP/1.1\r\nHost: registry.npmjs.org:8443\r\n\r\n")
		buf := make([]byte, 32)
		n, _ := c1.Read(buf)
		if n != 0 {
			t.Fatalf("non-TLS flow on intercepted port unexpectedly received %q", buf[:n])
		}
		_ = c1.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("HandleTransparent non-TLS did not exit")
		}
		if h.upstreamHits.Load() != hitsBefore {
			t.Fatalf("non-TLS flow on intercepted non-443 port bypassed interception and hit upstream")
		}
	})
}

func TestInterceptOverLinkStreamConnectionCloseFlushesResponse(t *testing.T) {
	h := newInterceptHarness(t, Config{
		Session: "devbox/link-close",
		Sandbox: SandboxConfig{Preset: PresetBalanced},
	}, true)

	supConn, egConn := net.Pipe()
	defer supConn.Close()
	defer egConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunOnConn(ctx, "link-close", egConn, &ServerOptions{
			Addrs:       h.egressSrv.Addrs,
			Dialer:      h.egressSrv.Dialer,
			UpstreamTLS: h.egressSrv.proxy.Tr.TLSClientConfig,
		})
	}()

	cl, err := NewClient(ctx, supConn, StartArgs{Session: "devbox/link-close"}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() {
		_ = cl.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("RunOnConn did not exit within timeout")
		}
	}()

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(cl.CACertPEM())) {
		t.Fatal("failed to parse session CA PEM")
	}

	// Send 20 consecutive HTTP/1.1 Connection: close requests over link.Stream.
	// crypto/tls.Conn.closeNotify calls SetWriteDeadline(time.Now()) right
	// before Close(); tlsCloseConn must clear it so link.Stream.Close lingers
	// and delivers every HTTP response and close_notify cleanly.
	for i := range 20 {
		st, err := cl.OpenEgress(ctx, StreamHeader{Via: "explicit"})
		if err != nil {
			t.Fatalf("iter %d: OpenEgress: %v", i, err)
		}
		_ = st.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(st, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nHost: registry.npmjs.org:443\r\n\r\n"); err != nil {
			_ = st.Close()
			t.Fatalf("iter %d: write CONNECT: %v", i, err)
		}
		br := bufio.NewReader(st)
		connResp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			_ = st.Close()
			t.Fatalf("iter %d: read CONNECT: %v", i, err)
		}
		_ = connResp.Body.Close()
		if connResp.StatusCode != http.StatusOK {
			_ = st.Close()
			t.Fatalf("iter %d: CONNECT status = %d, want 200", i, connResp.StatusCode)
		}

		tlsConn := tls.Client(st, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: "registry.npmjs.org",
			RootCAs:    roots,
			NextProtos: []string{"http/1.1"},
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = st.Close()
			t.Fatalf("iter %d: TLS handshake: %v", i, err)
		}
		reqPath := fmt.Sprintf("/pkg-%d", i)
		if _, err := fmt.Fprintf(tlsConn, "GET %s HTTP/1.1\r\nHost: registry.npmjs.org\r\nConnection: close\r\n\r\n", reqPath); err != nil {
			_ = tlsConn.Close()
			t.Fatalf("iter %d: write GET: %v", i, err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
		if err != nil {
			_ = tlsConn.Close()
			t.Fatalf("iter %d: ReadResponse failed (response dropped on TLS close): %v", i, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = tlsConn.Close()
		if readErr != nil {
			t.Fatalf("iter %d: read body: %v", i, readErr)
		}
		if resp.StatusCode != http.StatusOK || string(body) != "GET "+reqPath {
			t.Fatalf("iter %d: got %d %q, want 200 %q", i, resp.StatusCode, string(body), "GET "+reqPath)
		}
	}
}

func dialInterceptTLS(t *testing.T, proxyAddr, authority, sni string, roots *x509.CertPool, nextProtos []string) *tls.Conn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("write CONNECT: %v", err)
	}
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = raw.Close()
		t.Fatalf("CONNECT %s: err=%v", authority, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = raw.Close()
		t.Fatalf("CONNECT %s: status=%d, want 200", authority, resp.StatusCode)
	}
	tlsConn := tls.Client(raw, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: sni,
		RootCAs:    roots,
		NextProtos: nextProtos,
	})
	if err := tlsConn.Handshake(); err != nil {
		_ = raw.Close()
		t.Fatalf("TLS handshake %s: %v", sni, err)
	}
	return tlsConn
}
