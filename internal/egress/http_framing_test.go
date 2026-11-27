package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

func TestForbiddenTrailerDeclarationsDoNotDialOrSpendOnce(t *testing.T) {
	for _, field := range []string{
		"Host", "Proxy-Authorization", "Proxy-Authenticate", "Proxy-Other",
		"Authorization", "Connection", "Upgrade", "TE", "Transfer-Encoding",
		"Content-Length", "Trailer", "WWW-Authenticate", "Expect", "Content-Encoding",
		"Content-Type", "Content-Range", "Range", "Cache-Control", "Max-Forwards",
		"Pragma", "Via", "Warning", "If-Match", "Realm", "", "X-Good,,X-Other", "Bad@Name",
	} {
		t.Run(field, func(t *testing.T) {
			env, _, dials := startBodyHTTP(t)
			pol := pathTestPolicy(t, api.PresetLocked, "example.com:80/")
			pol.Rules[0].Once = true
			pol.Rules[0].GrantID = "trailer-once"
			env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
			wire := "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\nTrailer: " + field + "\r\n\r\n0\r\n\r\n"
			if status := pathHTTPExchange(t, env, wire); status != http.StatusBadRequest || dials.Load() != 0 {
				t.Fatalf("forbidden declaration returned %d after %d dials", status, dials.Load())
			}
			valid := "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\nContent-Length: 3\r\n\r\none"
			if status := pathHTTPExchange(t, env, valid); status != http.StatusOK || dials.Load() != 1 {
				t.Fatalf("invalid declaration spent the once grant: next status=%d dials=%d", status, dials.Load())
			}
			if status := pathHTTPExchange(t, env, valid); status != http.StatusForbidden || dials.Load() != 1 {
				t.Fatalf("successful upload did not spend its once grant: status=%d dials=%d", status, dials.Load())
			}
		})
	}
}

func TestTrailersRequireChunkedFramingBeforeDial(t *testing.T) {
	env, _, dials := startBodyHTTP(t)
	wire := "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\nContent-Length: 3\r\nTrailer: X-Checksum\r\n\r\none"
	if status := pathHTTPExchange(t, env, wire); status != http.StatusBadRequest || dials.Load() != 0 {
		t.Fatalf("non-chunked trailer returned %d after %d dials", status, dials.Load())
	}
}

func TestAbsoluteHTTPRejectsTheOriginalMismatchedHostBeforeDial(t *testing.T) {
	for _, field := range []string{"other.example.com", "example.com:81", ""} {
		t.Run(field, func(t *testing.T) {
			env, _, dials := startBodyHTTP(t)
			wire := "POST http://example.com/upload HTTP/1.1\r\nHost: " + field + "\r\nContent-Length: 3\r\n\r\none"
			if status := pathHTTPExchange(t, env, wire); status != http.StatusBadRequest || dials.Load() != 0 {
				t.Fatalf("original Host mismatch was hidden by URL authority: status=%d dials=%d", status, dials.Load())
			}
		})
	}
}

func TestUndeclaredAndForbiddenBodyTrailersNeverReachUpstream(t *testing.T) {
	for _, tc := range []struct{ declaration, field string }{
		{"Trailer: X-Checksum\r\n", "X-Original-URL"},
		{"Trailer: X-Checksum\r\n", "Host"},
		{"Trailer: X-Checksum\r\n", "Authorization"},
		{"", "X-Unannounced"},
	} {
		t.Run(tc.declaration+tc.field, func(t *testing.T) {
			env, seen, dials := startBodyHTTP(t)
			wire := "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n" + tc.declaration + "\r\n3\r\none\r\n0\r\n" + tc.field + ": /admin\r\n\r\n"
			if status := pathHTTPExchange(t, env, wire); status != http.StatusBadGateway || dials.Load() != 1 {
				t.Fatalf("invalid trailer completed: status=%d dials=%d", status, dials.Load())
			}
			got := <-seen
			if got.err == nil || got.trailers.Get(tc.field) != "" {
				t.Fatalf("upstream completed or received an undeclared field: error=%v trailers=%v", got.err, got.trailers)
			}
		})
	}
}

func TestCONNECTRejectsFramedBodiesBeforeDial(t *testing.T) {
	for _, framing := range []string{
		"Transfer-Encoding: chunked\r\n", "Content-Length: 1\r\n",
		"Content-Length: 00\r\n", "Trailer: X-Checksum\r\n",
	} {
		t.Run(framing, func(t *testing.T) {
			env, _, dials := startBodyHTTP(t)
			env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pathTestPolicy(t, api.PresetLocked, "example.com:80")})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "example.com", Port: 80})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			_ = st.SetDeadline(time.Now().Add(5 * time.Second))
			wire := "CONNECT example.com:80 HTTP/1.1\r\nHost: example.com:80\r\n" + framing + "\r\n"
			if _, err := io.WriteString(st, wire); err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(st), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusBadRequest || dials.Load() != 0 {
				t.Fatalf("CONNECT framing returned %d after %d dials", resp.StatusCode, dials.Load())
			}
		})
	}
}

func TestCONNECTLargeHeaderPreservesPipelinedClientHello(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	hello := captureClientHello(t, &tls.Config{ServerName: "example.com", InsecureSkipVerify: true})
	prefix := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nX-Padding: " + strings.Repeat("p", 5<<10) + "\r\n\r\n"
	done := make(chan error, 1)
	go func() { _, err := client.Write(append([]byte(prefix), hello...)); done <- err }()
	req, _, tail, err := readInitialHTTPRequest(server)
	if err != nil || req.Method != http.MethodConnect {
		t.Fatalf("large CONNECT parse: %v", err)
	}
	ch, err := ReadClientHello(tail)
	if err != nil || !bytes.Equal(ch.Raw, hello) {
		t.Fatalf("pipelined TLS bytes changed: %v", err)
	}
	if err := CheckClientHello(ch, "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFailedUpstreamWriteDoesNotDrainTheGuestBody(t *testing.T) {
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
	}
	env := startTestServer(t, d)
	env.call(t, CtlInit, InitArgs{SessionID: "drain234", Agent: "shell", Policy: pathTestPolicy(t, api.PresetLocked, "example.com:80/")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "example.com", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(st, "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\nContent-Length: 1000\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(st), nil)
	if err != nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("upstream failure waited to drain an unfinished guest body: %v", err)
	}
}

func TestPlainHTTPRemovesHopByHopTEHeader(t *testing.T) {
	env, seen, _ := startBodyHTTP(t)
	wire := "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\nTE: trailers\r\nContent-Length: 3\r\n\r\none"
	if status := pathHTTPExchange(t, env, wire); status != http.StatusOK {
		t.Fatalf("valid upload returned %d", status)
	}
	if got := <-seen; got.err != nil || got.header.Get("TE") != "" {
		t.Fatalf("upstream received hop-by-hop TE: error=%v headers=%v", got.err, got.header)
	}
}
