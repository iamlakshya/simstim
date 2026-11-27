package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const secretRouteReal = "RouteCredential0123456789AB"
const secretRouteName = "TEST_API_KEY"
const secretRouteAuthority = "api.example.com:443"

type secretRoutePhysicalOriginKey struct{}

type secretRouteFixture struct {
	control      *boundControlFixture
	initial      BoundInitResult
	client       *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	roots        *x509.CertPool
	mu           sync.Mutex
	closing      bool
	conns        []net.Conn
	serving      sync.WaitGroup
	nextID       uint64
	lookups      atomic.Int64
	dials        atomic.Int64
	originWrites atomic.Int64
	requests     atomic.Int64
	guestDials   atomic.Int64
	originCloses atomic.Int64
}

type secretRouteOriginConn struct {
	net.Conn
	writes    *atomic.Int64
	closes    *atomic.Int64
	closeOnce sync.Once
}

func (c *secretRouteOriginConn) Write(p []byte) (int, error) {
	c.writes.Add(int64(len(p)))
	return c.Conn.Write(p)
}

func (c *secretRouteOriginConn) Close() error {
	c.closeOnce.Do(func() { c.closes.Add(1) })
	return c.Conn.Close()
}

func secretRouteArgs(t *testing.T, pol api.Policy) BoundInitArgs {
	t.Helper()
	args := boundRegistryArgs(map[string]string{secretRouteName: secretRouteReal})
	args.Initial.Policy = pol
	args.Initial.DNS = DNSModeReal
	args.Binds[secretRouteName] = api.SecretBind{Hosts: []string{"api.example.com"}, Header: "Authorization", Format: "Bearer {value}"}
	return args
}

func secretRoutePolicy(t *testing.T, rules ...api.Rule) api.Policy {
	t.Helper()
	if len(rules) == 0 {
		// A trailing slash grants this fixture subtree. Explicit rules keep
		// their own exact path or prefix, including the once/admission cases.
		rules = []api.Rule{{Allow: true, Target: secretRouteAuthority + "/allowed/"}}
	}
	return onceTestPolicy(t, api.PresetLocked, rules...)
}

// Both sides use owned pipes. The documentation address is only an assertion
// token passed between the injected resolver and injected dialler.
func newSecretRouteFixture(t *testing.T, guestProtocol, originProtocol int, args BoundInitArgs, handler http.Handler) *secretRouteFixture {
	t.Helper()
	return newSecretRouteFixtureWithAck(t, guestProtocol, originProtocol, args, handler, true)
}

func newSecretRouteFixtureWithAck(t *testing.T, guestProtocol, originProtocol int, args BoundInitArgs, handler http.Handler, autoAck bool) *secretRouteFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	f := &secretRouteFixture{ctx: ctx, cancel: cancel, nextID: 2}
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.certificate("api.example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	originALPN := []string{"http/1.1"}
	if originProtocol == 2 {
		originALPN = []string{"h2", "http/1.1"}
	}
	originTLS := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}, NextProtos: originALPN}
	originRoots := x509.NewCertPool()
	originRoots.AddCert(ca.cert)
	dialer := &Dialer{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			f.lookups.Add(1)
			if host != "api.example.com" {
				return nil, errors.New("unexpected resolver authority")
			}
			return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
		},
		DialTCP: func(_ context.Context, addr netip.AddrPort) (net.Conn, error) {
			physical := f.dials.Add(1)
			if addr != netip.MustParseAddrPort("198.51.100.8:443") {
				return nil, errors.New("unexpected checked origin endpoint")
			}
			conn, err := f.pipe(func(server net.Conn) {
				originContext := context.WithValue(ctx, secretRoutePhysicalOriginKey{}, physical)
				serveHTTPConnection(originContext, tls.Server(server, originTLS), originTLS, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					f.requests.Add(1)
					expectedProtocol := originProtocol
					if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
						expectedProtocol = 1
					}
					if req.ProtoMajor != expectedProtocol || req.Host != secretRouteAuthority || req.TLS == nil || (expectedProtocol == 2 && req.TLS.NegotiatedProtocol != "h2") || (expectedProtocol == 1 && req.TLS.NegotiatedProtocol == "h2") {
						http.Error(w, "origin protocol or authority changed", http.StatusBadRequest)
						return
					}
					handler.ServeHTTP(w, req)
				}), nil)
			})
			if err != nil {
				return nil, err
			}
			return &secretRouteOriginConn{Conn: conn, writes: &f.originWrites, closes: &f.originCloses}, nil
		},
	}
	f.control = newBoundControlFixtureWithOptions(t, autoAck, Options{Dialer: dialer, OriginRoots: originRoots})
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(guestProtocol == 2)
	transport := &http.Transport{Protocols: protocols, ForceAttemptHTTP2: guestProtocol == 2, DisableCompression: true,
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return f.dialTLS(ctx, guestProtocol, "api.example.com", "api.example.com")
		},
	}
	f.client = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	t.Cleanup(func() {
		f.mu.Lock()
		f.closing = true
		all := append([]net.Conn(nil), f.conns...)
		f.mu.Unlock()
		cancel()
		transport.CloseIdleConnections()
		for _, conn := range all {
			_ = conn.Close()
		}
		_ = f.control.server.Close()
		f.serving.Wait()
	})
	f.call(t, CtlBoundInit, args, &f.initial)
	if f.initial.Version != MemberRegistryV2 || f.initial.Generation == "" || f.initial.CACert == "" {
		t.Fatal("real bound initialisation did not return public session state")
	}
	f.roots = x509.NewCertPool()
	if !f.roots.AppendCertsFromPEM([]byte(f.initial.CACert)) {
		t.Fatal("invalid public session root")
	}
	if state, exists := f.initial.Secrets[secretRouteName]; !exists || state.Sentinel == "" {
		t.Fatal("real bound initialisation omitted its public stand-in")
	}
	return f
}

func (f *secretRouteFixture) pipe(serve func(net.Conn)) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing {
		return nil, net.ErrClosed
	}
	client, server := net.Pipe()
	f.conns = append(f.conns, client, server)
	f.serving.Add(1)
	go func() { defer f.serving.Done(); defer server.Close(); serve(server) }()
	return client, nil
}

// Test calls use IDs 2 to 10. The fixture's separate public persistence ACK
// worker owns IDs above 10 and is the only other control writer.
func (f *secretRouteFixture) call(t *testing.T, op string, args, result any) {
	t.Helper()
	id := f.nextID
	f.nextID++
	if id > 10 {
		t.Fatal("route oracle exhausted its distinct control IDs")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.control.writer.WriteJSON(proto.TRequest, proto.Request{ID: id, Op: op, Args: raw}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-f.control.responses:
		if response.ID != id || !response.OK {
			t.Fatalf("actual control operation %s was refused or misrouted", op)
		}
		if result != nil && json.Unmarshal(response.Result, result) != nil {
			t.Fatal("malformed actual control result")
		}
	case err := <-f.control.failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual control operation did not finish")
	}
}

func (f *secretRouteFixture) dialTLS(ctx context.Context, protocol int, serverName, tunnelHost string) (net.Conn, error) {
	f.guestDials.Add(1)
	client, err := f.pipe(func(server net.Conn) {
		f.control.server.ServeFlow(f.ctx, server, StreamHeader{Via: "explicit", Host: tunnelHost, Port: 443})
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (net.Conn, error) { _ = client.Close(); return nil, err }
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := fmt.Fprintf(client, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", tunnelHost, tunnelHost); err != nil {
		return fail(err)
	}
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return fail(err)
	}
	if response.StatusCode != http.StatusOK {
		return fail(errors.New("local CONNECT was refused"))
	}
	alpn := []string{"http/1.1"}
	if protocol == 2 {
		alpn = []string{"h2", "http/1.1"}
	}
	conn := tls.Client(&readerConn{Conn: client, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots, ServerName: serverName, NextProtos: alpn})
	if err := conn.HandshakeContext(ctx); err != nil {
		return fail(err)
	}
	wantALPN := "http/1.1"
	if protocol == 2 {
		wantALPN = "h2"
	}
	if conn.ConnectionState().NegotiatedProtocol != wantALPN {
		return fail(errors.New("unexpected guest protocol"))
	}
	_ = client.SetDeadline(time.Time{})
	return conn, nil
}

func (f *secretRouteFixture) sentinel() string {
	return string(f.initial.Secrets[secretRouteName].Sentinel)
}

func (f *secretRouteFixture) audit(t *testing.T, method, path string) AuditEntry {
	t.Helper()
	for range 32 {
		entry := f.control.nextAudit(t)
		if entry.Method == method && entry.Path == path {
			return entry
		}
	}
	t.Fatal("actual audit did not identify the expected request")
	return AuditEntry{}
}

func (f *secretRouteFixture) requireNoOrigin(t *testing.T) {
	t.Helper()
	if f.lookups.Load() != 0 || f.dials.Load() != 0 || f.originWrites.Load() != 0 || f.requests.Load() != 0 {
		t.Fatalf("refusal reached origin: lookups=%d dials=%d bytes=%d requests=%d", f.lookups.Load(), f.dials.Load(), f.originWrites.Load(), f.requests.Load())
	}
}

func secretRouteRequest(t *testing.T, f *secretRouteFixture, method, path, credential string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "https://"+secretRouteAuthority+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if credential != "" {
		req.Header.Set("Authorization", credential)
	}
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func secretRouteRead(t *testing.T, response *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("guest response failed: %v", err)
	}
	return body
}
