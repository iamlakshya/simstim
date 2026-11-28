package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
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

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/brand"
)

func selfSignedTLSServer(t *testing.T, dnsNames ...string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(parsed)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, pool
}

type auditRecorder struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (a *auditRecorder) add(ev AuditEvent) {
	a.mu.Lock()
	a.events = append(a.events, ev)
	a.mu.Unlock()
}

func (a *auditRecorder) snapshot() []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditEvent(nil), a.events...)
}

func (a *auditRecorder) waitFor(t *testing.T, pred func(AuditEvent) bool) AuditEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range a.snapshot() {
			if pred(ev) {
				return ev
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for audit event; got %+v", a.snapshot())
	return AuditEvent{}
}

// startInMemoryEgress wires a Supervisor Client to RunOnConn over an in-memory
// pipe (zero listening TCP/UDP ports!) with injected resolver/dialer.
func startInMemoryEgress(t *testing.T, args StartArgs, res Resolver, dial DialFunc, hostAddrs []netip.Addr) (*Client, *auditRecorder) {
	t.Helper()
	ap, err := NewAddressPolicyWithHosts(hostAddrs)
	if err != nil {
		t.Fatal(err)
	}
	rec := &auditRecorder{}
	supConn, egConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		sd := &SafeDialer{Addrs: ap, Resolver: res, Dial: dial}
		done <- RunOnConn(ctx, "test-sess", egConn, &ServerOptions{
			Addrs:      ap,
			Dialer:     sd,
			SNITimeout: 2 * time.Second,
		})
	}()

	cli, err := NewClient(ctx, supConn, args, rec.add)
	if err != nil {
		cancel()
		_ = supConn.Close()
		_ = egConn.Close()
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cli.Close()
		<-done
	})
	return cli, rec
}

func proxyClientOverEgress(cli *Client, pool *x509.CertPool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return url.Parse("http://egress.local:3128")
			},
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return cli.OpenEgress(ctx, StreamHeader{Via: "explicit"})
			},
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 5 * time.Second,
	}
}

func TestEndToEndConnectAndHTTPThroughLink(t *testing.T) {
	tlsSrv, pool := selfSignedTLSServer(t, "github.com", "live.example")
	plainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain:" + r.URL.Path))
	}))
	t.Cleanup(plainSrv.Close)

	fakePub := ip4(93, 184, 216, 34)
	hostOwn := ip4(203, 0, 113, 9)
	res := &countingResolver{ips: map[string][]netip.Addr{
		"github.com":      {fakePub},
		"live.example":    {fakePub},
		"plain.example":   {fakePub},
		"imds.example":    {ip4(169, 254, 169, 254)},
		"hostown.example": {hostOwn},
	}}
	var upstreamDials atomic.Int32
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		upstreamDials.Add(1)
		switch addr {
		case "93.184.216.34:443":
			return (&net.Dialer{}).DialContext(ctx, network, tlsSrv.Listener.Addr().String())
		case "93.184.216.34:80":
			return (&net.Dialer{}).DialContext(ctx, network, plainSrv.Listener.Addr().String())
		default:
			return nil, net.UnknownNetworkError("unexpected dial to " + addr)
		}
	}

	policyTOML := `
session = "devbox/auth-fix"
agent = "claude"

[sandbox]
preset = "balanced"

[network]
allow = ["plain.example:80", "imds.example", "hostown.example"]

[lock]
deny = ["locked.example"]
`
	cli, rec := startInMemoryEgress(t, StartArgs{
		PolicyTOML:     policyTOML,
		ExposedSecrets: map[string]string{"MY_API_KEY": "super-secret-value-123"},
	}, res, dial, []netip.Addr{hostOwn})

	hc := proxyClientOverEgress(cli, pool)

	// 1. Balanced preset allows spliced HTTPS CONNECT to github.com with SNI check.
	resp, err := hc.Get("https://github.com/left-pad")
	if err != nil {
		t.Fatalf("GET https://github.com: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok:/left-pad" {
		t.Fatalf("HTTPS response = %d %q, want 200 ok:/left-pad", resp.StatusCode, body)
	}
	hc.CloseIdleConnections()
	ev := rec.waitFor(t, func(e AuditEvent) bool {
		return e.Host == "github.com" && e.Decision == "allow"
	})
	if ev.Mode != "splice" || ev.Up == 0 || ev.Down == 0 || ev.Rule != "preset:balanced" {
		t.Errorf("audit event for splice = %+v", ev)
	}

	// 2. Disallowed host gets HTTP 403 with exact grant hint.
	st, err := cli.OpenEgress(context.Background(), StreamHeader{Via: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(st, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	rawResp, err := http.ReadResponse(bufio.NewReader(st), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read 403 CONNECT response: %v", err)
	}
	denyBody, _ := io.ReadAll(rawResp.Body)
	_ = rawResp.Body.Close()
	_ = st.Close()
	wantHint := brand.Name + " policy allow example.com --in devbox/auth-fix"
	if rawResp.StatusCode != http.StatusForbidden || !strings.Contains(string(denyBody), wantHint) {
		t.Errorf("denied CONNECT = %d %q, want 403 containing %q", rawResp.StatusCode, denyBody, wantHint)
	}

	// 3. Name resolving to cloud metadata or host's own address gets 403 and zero dials.
	dialsBefore := upstreamDials.Load()
	for _, target := range []string{"imds.example:443", "hostown.example:443", "169.254.169.254:80"} {
		s, err := cli.OpenEgress(context.Background(), StreamHeader{Via: "explicit"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(s, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
		r, err := http.ReadResponse(bufio.NewReader(s), &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatalf("read response for %s: %v", target, err)
		}
		_ = r.Body.Close()
		_ = s.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("CONNECT %s = %d, want 403", target, r.StatusCode)
		}
	}
	if upstreamDials.Load() != dialsBefore {
		t.Errorf("refused IP triggered %d upstream dials, want 0", upstreamDials.Load()-dialsBefore)
	}

	// 4. SNI mismatch and ECH ClientHello on port 443 are refused without dialing upstream.
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"mismatched SNI", buildTestClientHello(t, "evil.example", 0, nil)},
		{"ECH ClientHello", buildTestClientHello(t, "github.com", extEncryptedClientHello, []byte{0, 0, 1, 0, 1, 0, 0, 0, 0, 0})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := upstreamDials.Load()
			s, err := cli.OpenEgress(context.Background(), StreamHeader{Via: "explicit"})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(s, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n")
			br := bufio.NewReader(s)
			r, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("CONNECT setup: %v", err)
			}
			_ = r.Body.Close()
			if r.StatusCode != http.StatusOK {
				t.Fatalf("CONNECT setup status = %d", r.StatusCode)
			}
			_, _ = s.Write(tc.raw)
			// Expect proxy to close stream without dialing upstream.
			buf := make([]byte, 16)
			_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _ := br.Read(buf)
			_ = s.Close()
			if n != 0 {
				t.Errorf("expected closed connection on %s, read %d bytes", tc.name, n)
			}
			if upstreamDials.Load() != before {
				t.Errorf("%s dialled upstream!", tc.name)
			}
		})
	}

	// 5. Plain HTTP request redacts exposed secret value in path to [NAME] and strips query.
	resp, err = hc.Get("http://plain.example/v1/super-secret-value-123/items?secret=never-logged")
	if err != nil {
		t.Fatalf("GET http://plain.example: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "plain:") {
		t.Fatalf("plain HTTP response = %d %q", resp.StatusCode, body)
	}
	httpEv := rec.waitFor(t, func(e AuditEvent) bool {
		return e.Host == "plain.example" && e.Mode == "http"
	})
	if httpEv.Path != "/v1/[MY_API_KEY]/items" {
		t.Errorf("redacted audit path = %q, want /v1/[MY_API_KEY]/items", httpEv.Path)
	}
	if strings.Contains(httpEv.Path, "super-secret") || strings.Contains(httpEv.Path, "never-logged") {
		t.Errorf("audit path leaked secret or query: %q", httpEv.Path)
	}

	// 6. Live policy Allow opens live.example, and locked.example stays refused.
	if err := cli.Allow(context.Background(), "locked.example"); err == nil {
		t.Fatal("Allow(locked.example) unexpectedly succeeded")
	}
	if err := cli.Allow(context.Background(), "live.example"); err != nil {
		t.Fatalf("Allow(live.example): %v", err)
	}
	resp, err = hc.Get("https://live.example/after-grant")
	if err != nil {
		t.Fatalf("GET https://live.example after live grant: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("live.example status = %d, want 200", resp.StatusCode)
	}

	// 7. Live policy Deny blocks live.example immediately for new flows.
	if err := cli.Deny(context.Background(), "live.example"); err != nil {
		t.Fatalf("Deny(live.example): %v", err)
	}
	hc.CloseIdleConnections()
	if deniedResp, err := hc.Get("https://live.example/after-deny"); err == nil {
		_ = deniedResp.Body.Close()
		t.Fatal("live.example succeeded after live Deny")
	}
}

func TestTransparentFlowWithSyntheticDNS(t *testing.T) {
	tlsSrv, pool := selfSignedTLSServer(t, "github.com")
	fakePub := ip4(93, 184, 216, 34)
	res := &countingResolver{ips: map[string][]netip.Addr{"github.com": {fakePub}}}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, tlsSrv.Listener.Addr().String())
	}

	cli, _ := startInMemoryEgress(t, StartArgs{
		PolicyTOML: "[sandbox]\npreset = \"balanced\"\n",
		DNSMode:    DNSModeSynthetic,
	}, res, dial, nil)

	// Resolve github.com over a KindDNS stream to get its synthetic IP.
	dnsStream, err := cli.OpenDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q := buildTestDNSQuery(t, 42, "github.com.", 1 /* TypeA */)
	hdr := []byte{byte(len(q) >> 8), byte(len(q))}
	_, _ = dnsStream.Write(hdr)
	_, _ = dnsStream.Write(q)
	_ = dnsStream.CloseWrite()
	var rlen [2]byte
	if _, err := io.ReadFull(dnsStream, rlen[:]); err != nil {
		t.Fatal(err)
	}
	rawResp := make([]byte, int(rlen[0])<<8|int(rlen[1]))
	if _, err := io.ReadFull(dnsStream, rawResp); err != nil {
		t.Fatal(err)
	}
	_ = dnsStream.Close()
	_, ans := parseDNSResponse(t, rawResp)
	if len(ans) != 1 {
		t.Fatalf("synthetic DNS answers = %d, want 1", len(ans))
	}
	synthIP := netip.AddrFrom4(ans[0].Body.(*dnsmessage.AResource).A)

	// Open a transparent stream naming that synthetic IP and complete a real TLS GET.
	st, err := cli.OpenEgress(context.Background(), StreamHeader{Via: "transparent", Host: synthIP.String(), Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	tlsConn := tls.Client(st, &tls.Config{ServerName: "github.com", RootCAs: pool})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("transparent TLS handshake: %v", err)
	}
	_, _ = io.WriteString(tlsConn, "GET /synth HTTP/1.1\r\nHost: github.com\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
	if err != nil {
		t.Fatalf("read transparent HTTP response: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "ok:/synth" {
		t.Errorf("transparent response = %q, want ok:/synth", b)
	}
}

func TestRedactorPathTruncationAndUTF8(t *testing.T) {
	r := NewRedactor(map[string]string{
		"SHORT": "abc",
		"LONG":  "abcdef",
	})
	// Longer secret wins over shorter substring.
	if got := r.Path("/p/abcdef/x?token=abc#frag"); got != "/p/[LONG]/x" {
		t.Errorf("Path = %q, want /p/[LONG]/x", got)
	}
	// Truncates at valid UTF-8 boundary under 256 bytes.
	longPath := "/" + strings.Repeat("é", 200)
	got := r.Path(longPath)
	if len(got) > maxAuditPathBytes {
		t.Errorf("len(Path) = %d, want <= %d", len(got), maxAuditPathBytes)
	}
	if !bytes.Equal([]byte(got), []byte(strings.ToValidUTF8(got, ""))) {
		t.Error("truncated path split a UTF-8 rune")
	}
}

func FuzzStreamHeader(f *testing.F) {
	f.Add([]byte(`{"via":"explicit"}`))
	f.Add([]byte(`{"via":"transparent","host":"api.anthropic.com","port":443}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		var hdr StreamHeader
		if err := json.Unmarshal(data, &hdr); err == nil {
			_, _ = normalizeHost(hdr.Host)
		}
	})
}

func BenchmarkSplicedSetupAndThroughput(b *testing.B) {
	// Measure spliced setup and throughput through the goproxy egress server
	// over an in-memory link against a local TLS server (spec 12 targets:
	// <= 3 ms setup, >= 200 MiB/s throughput).
	t := &testing.T{}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bench.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"bench.example"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		b.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(der)
	pool.AddCert(parsed)
	_ = t

	payload := bytes.Repeat([]byte("a"), 1<<20) // 1 MiB
	upSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write(payload)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	upSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	upSrv.StartTLS()
	defer upSrv.Close()

	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		b.Fatal(err)
	}
	eng, err := NewEngine(Config{Sandbox: SandboxConfig{Preset: PresetOpen}})
	if err != nil {
		b.Fatal(err)
	}
	res := &countingResolver{ips: map[string][]netip.Addr{"bench.example": {ip4(93, 184, 216, 34)}}}
	sd := &SafeDialer{
		Addrs:    ap,
		Engine:   eng,
		Resolver: res,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
		},
	}
	srv, err := NewServer(ServerOptions{Engine: eng, Addrs: ap, Dialer: sd})
	if err != nil {
		b.Fatal(err)
	}
	proxySrv := httptest.NewServer(srv)
	defer proxySrv.Close()
	proxyURL, _ := url.Parse(proxySrv.URL)

	b.Run("setup", func(b *testing.B) {
		hc := &http.Client{
			Transport: &http.Transport{
				Proxy:             http.ProxyURL(proxyURL),
				DisableKeepAlives: true,
				TLSClientConfig:   &tls.Config{RootCAs: pool},
			},
		}
		b.ResetTimer()
		for range b.N {
			resp, err := hc.Get("https://bench.example/ping")
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})

	b.Run("throughput", func(b *testing.B) {
		hc := &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{RootCAs: pool},
			},
		}
		b.SetBytes(int64(len(payload)))
		b.ResetTimer()
		for range b.N {
			resp, err := hc.Get("https://bench.example/big")
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})
}
