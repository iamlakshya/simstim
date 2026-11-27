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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func newPhysicalModelLifetimeFixture(t *testing.T, guestH2, originH2 bool, handler http.Handler) *httpLifetimeFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.certificate("api.openai.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	originTLS := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}, NextProtos: []string{"h2", "http/1.1"}}
	if !originH2 {
		originTLS.NextProtos = []string{"http/1.1"}
	}
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
	f.env = startTestServer(t, dialer)
	originRoots := x509.NewCertPool()
	originRoots.AddCert(ca.cert)
	f.env.srv.transport.TLSClientConfig.RootCAs = originRoots
	f.env.call(t, CtlInit, InitArgs{Agent: "codex", Policy: api.Policy{Preset: api.PresetLocked}})
	guestRoots := x509.NewCertPool()
	guestRoots.AddCert(f.env.srv.ca.cert)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(guestH2)
	transport := &http.Transport{Protocols: protocols, ForceAttemptHTTP2: guestH2,
		DialTLSContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			f.guestDials.Add(1)
			client, err := pipe(func(server net.Conn) {
				f.env.srv.ServeFlow(ctx, server, StreamHeader{Via: "explicit", Host: "api.openai.com", Port: 443})
			})
			if err != nil {
				return nil, err
			}
			fail := func(err error) (net.Conn, error) { _ = client.Close(); return nil, err }
			if _, err := io.WriteString(client, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n"); err != nil {
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
			config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: guestRoots, ServerName: "api.openai.com", NextProtos: []string{"http/1.1"}}
			if guestH2 {
				config.NextProtos = []string{"h2", "http/1.1"}
			}
			conn := tls.Client(&readerConn{Conn: client, reader: reader}, config)
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

func TestPhysicalModelProductionComposesBothGuestAndOriginHTTPVersions(t *testing.T) {
	for _, guestH2 := range []bool{false, true} {
		for _, originH2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("guestH2%t-originH2%t", guestH2, originH2), func(t *testing.T) {
				const requestBody = `{"model":"model-a","input":"original"}`
				const answer = `{"usage":{"total_tokens":7}}`
				originResult := make(chan error, 1)
				fixture := newPhysicalModelLifetimeFixture(t, guestH2, originH2, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					body, err := io.ReadAll(request.Body)
					wantMajor := 1
					if originH2 {
						wantMajor = 2
					}
					if err != nil || string(body) != requestBody || request.ProtoMajor != wantMajor || request.TLS == nil {
						originResult <- fmt.Errorf("origin request/TLS changed: %v", err)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, err = io.WriteString(w, answer)
					originResult <- err
				}))
				request, err := http.NewRequestWithContext(t.Context(), "POST", "https://api.openai.com/v1/responses", strings.NewReader(requestBody))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := fixture.client.Do(request)
				if err != nil {
					t.Fatal("actual production guest path failed", err)
				}
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				wantMajor := 1
				if guestH2 {
					wantMajor = 2
				}
				if err != nil || string(body) != answer || response.StatusCode != 200 || response.ProtoMajor != wantMajor {
					t.Fatal("production guest response changed", err)
				}
				if err := <-originResult; err != nil {
					t.Fatal(err)
				}
				waitHTTPState(t, func() bool { return fixture.env.srv.modelUsageSnapshot().Total.Requests == 1 }, "the original physical completion callback")
				usage := fixture.env.srv.modelUsageSnapshot()
				if usage.Total.TotalTokens != 7 || usage.Total.EstimatedRequests != 0 || fixture.dials.Load() != 1 || fixture.guestDials.Load() != 1 {
					t.Fatal("physical composition lost usage, changed ALPN or dialled twice")
				}
				waitHTTPState(t, func() bool { return len(fixture.env.srv.httpSem) == 0 }, "the original worker slot after native-write settlement")
			})
		}
	}
}
