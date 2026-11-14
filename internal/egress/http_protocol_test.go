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
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTPProxyPreservesGuestAndOriginProtocolsWithFreshRequestDials(t *testing.T) {
	for _, guestProtocol := range []int{1, 2} {
		for _, originProtocol := range []int{1, 2} {
			t.Run(fmt.Sprintf("guest%d_origin%d", guestProtocol, originProtocol), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				originCA, err := newSessionAuthority(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				leaf, err := originCA.certificate("api.example.com", time.Now())
				if err != nil {
					t.Fatal(err)
				}
				alpn := []string{"http/1.1"}
				if originProtocol == 2 {
					alpn = []string{"h2", "http/1.1"}
				}
				originTLS := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}, NextProtos: alpn}
				var originDials, originRequests, badProtocol atomic.Int64
				var origins sync.WaitGroup
				var originMu sync.Mutex
				originClosing := false
				dialer := &Dialer{
					Lookup: func(context.Context, string) ([]netip.Addr, error) {
						return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
					},
					DialTCP: func(_ context.Context, addr netip.AddrPort) (net.Conn, error) {
						if addr != netip.MustParseAddrPort("198.51.100.8:443") {
							return nil, fmt.Errorf("unexpected checked endpoint %s", addr)
						}
						originDials.Add(1)
						client, server := net.Pipe()
						originMu.Lock()
						if originClosing {
							originMu.Unlock()
							_ = client.Close()
							_ = server.Close()
							return nil, net.ErrClosed
						}
						origins.Go(func() {
							serveHTTPConnection(ctx, tls.Server(server, originTLS), originTLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								originRequests.Add(1)
								if r.ProtoMajor != originProtocol || r.Host != "api.example.com:443" || r.URL.Path != "/allowed" || r.URL.RawQuery != "q=ok" {
									badProtocol.Add(1)
								}
								_, _ = io.WriteString(w, "checked")
							}), nil)
							_ = server.Close()
						})
						originMu.Unlock()
						return client, nil
					},
				}
				env := startTestServer(t, dialer)
				roots := x509.NewCertPool()
				roots.AddCert(originCA.cert)
				env.srv.transport.TLSClientConfig.RootCAs = roots
				env.call(t, CtlInit, InitArgs{Policy: api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.example.com/allowed"}}}})
				guestRoots := x509.NewCertPool()
				guestRoots.AddCert(env.srv.ca.cert)
				var guestConnections atomic.Int64
				protocols := new(http.Protocols)
				protocols.SetHTTP1(true)
				protocols.SetHTTP2(guestProtocol == 2)
				transport := &http.Transport{Protocols: protocols, ForceAttemptHTTP2: guestProtocol == 2,
					DialTLSContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
						guestConnections.Add(1)
						client, server := net.Pipe()
						go env.srv.ServeFlow(ctx, server, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 443})
						if _, err := io.WriteString(client, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n"); err != nil {
							_ = client.Close()
							return nil, err
						}
						reader := bufio.NewReader(client)
						response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
						if err != nil || response.StatusCode != http.StatusOK {
							_ = client.Close()
							return nil, fmt.Errorf("CONNECT failed: %v", err)
						}
						guestALPN := []string{"http/1.1"}
						if guestProtocol == 2 {
							guestALPN = []string{"h2", "http/1.1"}
						}
						conn := tls.Client(&readerConn{Conn: client, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: guestRoots, ServerName: "api.example.com", NextProtos: guestALPN})
						if err := conn.HandshakeContext(dctx); err != nil {
							_ = conn.Close()
							return nil, err
						}
						return conn, nil
					},
				}
				client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
				defer func() {
					transport.CloseIdleConnections()
					originMu.Lock()
					originClosing = true
					originMu.Unlock()
					cancel()
					_ = env.srv.Close()
					origins.Wait()
				}()
				for range 3 {
					response, err := client.Get("https://api.example.com/allowed?q=ok")
					if err != nil {
						t.Fatal(err)
					}
					body, readErr := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if readErr != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != guestProtocol || strings.TrimSpace(string(body)) != "checked" {
						t.Fatalf("guest response protocol/body = %s %d %q, %v", response.Proto, response.StatusCode, body, readErr)
					}
				}
				if originRequests.Load() != 3 || originDials.Load() != 3 || guestConnections.Load() != 1 || badProtocol.Load() != 0 {
					t.Fatalf("requests=%d checked dials=%d guest tunnels=%d wrong origin forms=%d", originRequests.Load(), originDials.Load(), guestConnections.Load(), badProtocol.Load())
				}
				for _, entry := range env.waitAudits(t, 3) {
					if entry.Mode != "intercept" || entry.Method != "GET" || entry.Path != "/allowed" || entry.Status != http.StatusOK || entry.Decision != "allow" || entry.Up <= 0 || entry.Down <= 0 {
						t.Fatalf("request audit lost canonical identity, verdict or origin bytes: %+v", entry)
					}
				}
			})
		}
	}
}
