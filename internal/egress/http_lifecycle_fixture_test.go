package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type httpLifetimeFixture struct {
	env        *testEnv
	client     *http.Client
	dials      atomic.Int64
	guestDials atomic.Int64
}

// Every origin and guest connection is an owned pipe. The fixture opens no
// port and never asks the machine's DNS resolver about the synthetic host.
func newHTTPLifetimeFixture(t *testing.T, pol api.Policy, handler http.Handler, clock func() time.Time) *httpLifetimeFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.certificate("api.example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	originTLS := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}, NextProtos: []string{"h2", "http/1.1"}}
	f := new(httpLifetimeFixture)
	var mu sync.Mutex
	var conns []net.Conn
	var serving sync.WaitGroup
	closing := false
	pipe := func(serve func(net.Conn)) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if closing {
			return nil, net.ErrClosed
		}
		client, server := net.Pipe()
		conns = append(conns, client, server)
		serving.Add(1)
		go func() { defer serving.Done(); defer server.Close(); serve(server) }()
		return client, nil
	}
	dialer := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
		},
		DialTCP: func(_ context.Context, ap netip.AddrPort) (net.Conn, error) {
			if ap != netip.MustParseAddrPort("198.51.100.8:443") {
				return nil, fmt.Errorf("unexpected checked origin %s", ap)
			}
			f.dials.Add(1)
			return pipe(func(server net.Conn) {
				serveHTTPConnection(ctx, tls.Server(server, originTLS), originTLS, handler, nil)
			})
		},
	}
	f.env = startTestServerWithClock(t, dialer, clock)
	originRoots := x509.NewCertPool()
	originRoots.AddCert(ca.cert)
	f.env.srv.transport.TLSClientConfig.RootCAs = originRoots
	f.env.call(t, CtlInit, InitArgs{Policy: pol})
	guestRoots := x509.NewCertPool()
	guestRoots.AddCert(f.env.srv.ca.cert)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	transport := &http.Transport{Protocols: protocols, ForceAttemptHTTP2: true,
		DialTLSContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			f.guestDials.Add(1)
			client, err := pipe(func(server net.Conn) {
				f.env.srv.ServeFlow(ctx, server, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 443})
			})
			if err != nil {
				return nil, err
			}
			fail := func(err error) (net.Conn, error) { _ = client.Close(); return nil, err }
			if _, err := io.WriteString(client, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n"); err != nil {
				return fail(err)
			}
			reader := bufio.NewReader(client)
			response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
			if err != nil {
				return fail(err)
			}
			if response.StatusCode != http.StatusOK {
				return fail(fmt.Errorf("CONNECT returned %d", response.StatusCode))
			}
			conn := tls.Client(&readerConn{Conn: client, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: guestRoots, ServerName: "api.example.com", NextProtos: []string{"h2", "http/1.1"}})
			if err := conn.HandshakeContext(dctx); err != nil {
				return fail(err)
			}
			return conn, nil
		},
	}
	f.client = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	t.Cleanup(func() {
		mu.Lock()
		closing = true
		all := append([]net.Conn(nil), conns...)
		mu.Unlock()
		cancel()
		transport.CloseIdleConnections()
		for _, conn := range all {
			_ = conn.Close()
		}
		_ = f.env.srv.Close()
		serving.Wait()
	})
	return f
}

func waitHTTPProgress(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for " + name)
	}
}

func requireHTTPPrefix(t *testing.T, response *http.Response, want string) {
	t.Helper()
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 2 {
		t.Fatalf("expected active HTTP/2 response, got %s %d", response.Proto, response.StatusCode)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(response.Body, got); err != nil || string(got) != want {
		t.Fatalf("response prefix %q, want %q: %v", got, want, err)
	}
}

func waitHTTPState(t *testing.T, ready func() bool, name string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("timed out waiting for " + name)
		}
	}
}
