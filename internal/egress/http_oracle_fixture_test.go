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
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

// These fixtures use only an owned loopback origin and an inherited link.
// Each TLS hop has its own explicitly installed test root.
type httpProtocolOracle struct {
	env        *testEnv
	origin     *httptest.Server
	guestRoots *x509.CertPool
	dials      atomic.Int64
	lookups    atomic.Int64
}

func newHTTPProtocolOracle(t *testing.T, originH2 bool, handler http.Handler) *httpProtocolOracle {
	t.Helper()
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.certificate("api.example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	alpn := []string{"http/1.1"}
	if originH2 {
		alpn = []string{"h2", "http/1.1"}
	}
	origin := httptest.NewUnstartedServer(handler)
	origin.EnableHTTP2 = originH2
	origin.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}, NextProtos: alpn}
	origin.StartTLS()
	t.Cleanup(origin.Close)
	h := &httpProtocolOracle{origin: origin}
	dialer := &Dialer{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			h.lookups.Add(1)
			if host != "api.example.com" {
				return nil, fmt.Errorf("unexpected oracle DNS name %q", host)
			}
			return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
		},
		DialTCP: func(ctx context.Context, address netip.AddrPort) (net.Conn, error) {
			if address != netip.MustParseAddrPort("198.51.100.8:443") {
				return nil, fmt.Errorf("unchecked oracle endpoint %s", address)
			}
			h.dials.Add(1)
			var owned net.Dialer
			return owned.DialContext(ctx, "tcp", origin.Listener.Addr().String())
		},
	}
	h.env = startTestServer(t, dialer)
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	h.env.srv.transport.TLSClientConfig.RootCAs = roots
	h.env.call(t, CtlInit, InitArgs{SessionID: "protocol2345", Agent: "shell", Policy: pathTestPolicy(t, api.PresetLocked, "api.example.com/allowed/")})
	h.guestRoots = x509.NewCertPool()
	h.guestRoots.AddCert(h.env.srv.ca.cert)
	return h
}

type protocolOracleReaderConn struct {
	net.Conn
	reader io.Reader
}

func (c *protocolOracleReaderConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (h *httpProtocolOracle) guestTLS(ctx context.Context, protocol int) (*tls.Conn, error) {
	stream, err := h.env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 443})
	if err != nil {
		return nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(stream, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n"); err != nil {
		_ = stream.Close()
		return nil, err
	}
	reader := bufio.NewReader(stream)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = stream.Close()
		return nil, fmt.Errorf("oracle CONNECT returned %d", response.StatusCode)
	}
	alpn := []string{"http/1.1"}
	if protocol == 2 {
		alpn = []string{"h2", "http/1.1"}
	}
	conn := tls.Client(&protocolOracleReaderConn{Conn: stream, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: h.guestRoots, ServerName: "api.example.com", NextProtos: alpn})
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (h *httpProtocolOracle) client(t *testing.T, protocol int) *http.Client {
	t.Helper()
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(protocol == 2)
	transport := &http.Transport{Protocols: protocols, ForceAttemptHTTP2: protocol == 2, DisableCompression: true,
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return h.guestTLS(ctx, protocol) },
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 20 * time.Second}
}

func protocolOracleRequestError(r *http.Request, protocol int, method, path string) error {
	if r.ProtoMajor != protocol || r.Host != "api.example.com:443" || r.Method != method || r.URL.Path != path || r.URL.RawQuery != "kept=yes" {
		return fmt.Errorf("wrong canonical origin request: %s %s host=%q method=%q path=%q query=%q", r.Proto, r.RequestURI, r.Host, r.Method, r.URL.Path, r.URL.RawQuery)
	}
	if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		return errors.New("proxy credentials or forwarding identity reached the origin")
	}
	return nil
}

func requireProtocolOracleAudit(t *testing.T, h *httpProtocolOracle, method, path string, status int) {
	t.Helper()
	entries := h.env.waitAudits(t, 1)
	for _, entry := range entries {
		if entry.Method == method && entry.Path == path && entry.Mode == "intercept" {
			if entry.Status != status || entry.Decision != "allow" || entry.Up <= 0 || entry.Down <= 0 {
				t.Fatalf("incomplete protocol audit: %+v", entry)
			}
			return
		}
	}
	t.Fatalf("no per-request canonical protocol audit: %+v", entries)
}
