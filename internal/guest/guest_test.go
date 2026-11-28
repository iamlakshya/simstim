package guest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func TestGuestProxyAndDNSStubEndToEnd(t *testing.T) {
	// Start a local TLS upstream representing registry.npmjs.org.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "registry.npmjs.org"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"registry.npmjs.org"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(der)
	pool.AddCert(parsed)
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("npm-ok"))
	}))
	up.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	up.StartTLS()
	defer up.Close()

	// Wire egress.RunOnConn to egress.Client over an in-memory pipe.
	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := netip.AddrFrom4([4]byte{93, 184, 216, 34})
	sd := &egress.SafeDialer{
		Addrs:    ap,
		Resolver: staticResolver{"registry.npmjs.org": {fakePub}},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, up.Listener.Addr().String())
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	supConn, egConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- egress.RunOnConn(ctx, "guest-test", egConn, &egress.ServerOptions{
			Addrs:       ap,
			Dialer:      sd,
			UpstreamTLS: &tls.Config{RootCAs: pool},
		})
	}()
	cli, err := egress.NewClient(ctx, supConn, egress.StartArgs{
		PolicyTOML: "session = \"box/g1\"\n[sandbox]\npreset = \"balanced\"\n",
		DNSMode:    egress.DNSModeReal,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cli.Close()
		<-done
	}()

	// Start guest proxy shim on an ephemeral loopback port.
	gp, err := ListenProxy(ctx, cli, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go gp.Serve(ctx)
	defer func() { _ = gp.Close() }()

	// Start guest DNS stub on an ephemeral loopback UDP port.
	gd, err := ListenDNS(ctx, cli, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go gd.Serve(ctx)
	defer func() { _ = gd.Close() }()

	// 1. HTTPS CONNECT through guest.Proxy reaches allowed registry.npmjs.org
	// intercepted with the session CA.
	guestPool := pool.Clone()
	if !guestPool.AppendCertsFromPEM([]byte(cli.CACertPEM())) {
		t.Fatal("failed to parse session CA cert PEM")
	}
	proxyURL, _ := url.Parse("http://" + gp.Addr().String())
	hc := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: guestPool},
		},
		Timeout: 5 * time.Second,
	}
	resp, err := hc.Get("https://registry.npmjs.org/")
	if err != nil {
		t.Fatalf("GET via guest.Proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "npm-ok" {
		t.Fatalf("response = %d %q, want 200 npm-ok", resp.StatusCode, body)
	}

	// 2. UDP DNS query through guest.DNSStub resolves allowed host and NXDOMAINs denied host.
	udpConn, err := net.DialTimeout("udp", gd.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = udpConn.Close() }()
	_ = udpConn.SetDeadline(time.Now().Add(3 * time.Second))

	for _, tc := range []struct {
		name      string
		wantRCode dnsmessage.RCode
		wantAns   int
	}{
		{"registry.npmjs.org.", dnsmessage.RCodeSuccess, 1},
		{"example.com.", dnsmessage.RCodeNameError, 0},
	} {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
		_ = b.StartQuestions()
		_ = b.Question(dnsmessage.Question{
			Name:  dnsmessage.MustNewName(tc.name),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		})
		q, _ := b.Finish()
		if _, err := udpConn.Write(q); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 1024)
		n, err := udpConn.Read(buf)
		if err != nil {
			t.Fatalf("DNS read for %s: %v", tc.name, err)
		}
		var p dnsmessage.Parser
		hdr, err := p.Start(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		_ = p.SkipAllQuestions()
		ans, _ := p.AllAnswers()
		if hdr.RCode != tc.wantRCode || len(ans) != tc.wantAns {
			t.Errorf("DNS %s: rcode=%v ans=%d, want rcode=%v ans=%d", tc.name, hdr.RCode, len(ans), tc.wantRCode, tc.wantAns)
		}
	}
}

func TestProxyEnv(t *testing.T) {
	a := agents.Agent{
		Kind:     "custom",
		ProxyEnv: map[string]string{"CUSTOM_PROXY_FLAG": "1"},
	}
	env := ProxyEnv("http://127.0.0.1:3128", a)
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if env[k] != "http://127.0.0.1:3128" {
			t.Errorf("env[%s] = %q, want http://127.0.0.1:3128", k, env[k])
		}
	}
	if env["NODE_USE_ENV_PROXY"] != "1" {
		t.Errorf("NODE_USE_ENV_PROXY = %q, want 1", env["NODE_USE_ENV_PROXY"])
	}
	if env["NO_PROXY"] != "localhost,127.0.0.1,::1" || env["no_proxy"] != "localhost,127.0.0.1,::1" {
		t.Errorf("NO_PROXY = %q / %q", env["NO_PROXY"], env["no_proxy"])
	}
	if env["CUSTOM_PROXY_FLAG"] != "1" {
		t.Errorf("CUSTOM_PROXY_FLAG = %q, want 1", env["CUSTOM_PROXY_FLAG"])
	}
}

type noopOpener struct{}

func (noopOpener) OpenEgress(context.Context, egress.StreamHeader) (*link.Stream, error) {
	return nil, net.ErrClosed
}

func (noopOpener) OpenDNS(context.Context) (*link.Stream, error) {
	return nil, net.ErrClosed
}

func TestProxyAndDNSStubCloseBeforeContextEnds(t *testing.T) {
	// Pass a cancellable context that stays open while Close() stops Serve,
	// verifying AfterFunc unregisters cleanly and Serve returns promptly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gp, err := ListenProxy(ctx, noopOpener{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		gp.Serve(ctx)
	}()
	if err := gp.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Proxy.Serve did not return after Close")
	}

	gd, err := ListenDNS(ctx, noopOpener{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dnsDone := make(chan struct{})
	go func() {
		defer close(dnsDone)
		gd.Serve(ctx)
	}()
	if err := gd.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dnsDone:
	case <-time.After(2 * time.Second):
		t.Fatal("DNSStub.Serve did not return after Close")
	}
}
