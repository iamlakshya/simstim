package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type receivedBody struct {
	body, trailer, uri string
	header             http.Header
	trailers           http.Header
	err                error
}

func startBodyHTTP(t *testing.T) (*testEnv, <-chan receivedBody, *atomic.Int32) {
	t.Helper()
	seen := make(chan receivedBody, 1)
	dials := &atomic.Int32{}
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			dials.Add(1)
			client, upstream := net.Pipe()
			go func() {
				defer upstream.Close()
				_ = upstream.SetDeadline(time.Now().Add(5 * time.Second))
				r, err := http.ReadRequest(bufio.NewReader(upstream))
				if err != nil {
					seen <- receivedBody{err: err}
					return
				}
				body, err := io.ReadAll(r.Body)
				_ = r.Body.Close()
				seen <- receivedBody{body: string(body), trailer: r.Trailer.Get("X-Checksum"), header: r.Header.Clone(), trailers: r.Trailer.Clone(), uri: r.RequestURI, err: err}
				_, _ = io.WriteString(upstream, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}()
			return client, nil
		},
	}
	env := startTestServer(t, d)
	env.call(t, CtlInit, InitArgs{SessionID: "body2345", Agent: "shell", Policy: pathTestPolicy(t, api.PresetLocked, "example.com:80/")})
	return env, seen, dials
}

func TestPlainHTTPForwardsChunkedBodiesAndTrailers(t *testing.T) {
	for _, tc := range []struct{ name, header, wire, want, trailer string }{
		{"chunked", "Transfer-Encoding: chunked\r\n", "6\r\nhello \r\nd\r\nchunked world\r\n0\r\n\r\n", "hello chunked world", ""},
		{"case", "Transfer-Encoding: CHUNKED\r\n", "3\r\none\r\n0\r\n\r\n", "one", ""},
		{"extensions", "Transfer-Encoding: chunked\r\nTrailer: X-Checksum\r\n", "3;extension=yes\r\none\r\n0\r\nX-Checksum: digest\r\n\r\n", "one", "digest"},
		{"large", "Transfer-Encoding: chunked\r\n", fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", 128<<10, strings.Repeat("x", 128<<10)), strings.Repeat("x", 128<<10), ""},
		{"fixed", "Content-Length: 3\r\n", "one", "one", ""},
		{"empty", "Transfer-Encoding: chunked\r\n", "0\r\n\r\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, seen, dials := startBodyHTTP(t)
			request := "POST http://example.com/%75pload?query=unchanged HTTP/1.1\r\nHost: example.com\r\n" + tc.header + "\r\n" + tc.wire
			if status := pathHTTPExchange(t, env, request); status != http.StatusOK {
				t.Fatalf("upload returned HTTP %d", status)
			}
			got := <-seen
			if got.err != nil || got.body != tc.want || got.trailer != tc.trailer || got.uri != "/upload?query=unchanged" || dials.Load() != 1 {
				t.Fatalf("upstream body bytes=%d, trailer=%q, uri=%q, error=%v, dials=%d; want body bytes=%d, trailer=%q", len(got.body), got.trailer, got.uri, got.err, dials.Load(), len(tc.want), tc.trailer)
			}
		})
	}
}

func TestPlainHTTPRejectsAmbiguousTransferCodingBeforeDial(t *testing.T) {
	for _, header := range []string{
		"Transfer-Encoding: gzip\r\n", "Transfer-Encoding: gzip, chunked\r\n",
		"Transfer-Encoding: chunked, chunked\r\n", "Transfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n",
		"Transfer-Encoding: chunked\r\nContent-Length: 3\r\n", "Transfer-Encoding: identity\r\n",
	} {
		t.Run(strings.ReplaceAll(header, "\r\n", ";"), func(t *testing.T) {
			env, _, dials := startBodyHTTP(t)
			if status := pathHTTPExchange(t, env, "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\n"+header+"\r\n0\r\n\r\n"); status != http.StatusBadRequest || dials.Load() != 0 {
				t.Fatalf("ambiguous framing returned %d after %d upstream dials", status, dials.Load())
			}
		})
	}
}

func TestPlainHTTPRejectsHTTP10TransferCodingBeforeDial(t *testing.T) {
	env, _, dials := startBodyHTTP(t)
	request := "POST http://example.com/upload HTTP/1.0\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n3\r\none\r\n0\r\n\r\n"
	if status := pathHTTPExchange(t, env, request); status != http.StatusBadRequest || dials.Load() != 0 {
		t.Fatalf("HTTP/1.0 transfer coding returned %d after %d upstream dials", status, dials.Load())
	}
}

func TestPlainHTTPRejectsMalformedChunkFraming(t *testing.T) {
	for _, wire := range []string{"not-hex\r\none\r\n0\r\n\r\n", "3\r\noneXX0\r\n\r\n"} {
		t.Run(wire, func(t *testing.T) {
			env, seen, _ := startBodyHTTP(t)
			request := "POST http://example.com/upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n" + wire
			if status := pathHTTPExchange(t, env, request); status != http.StatusBadGateway {
				t.Fatalf("invalid chunk returned HTTP %d", status)
			}
			if got := <-seen; got.err == nil {
				t.Fatal("malformed chunk completed an upstream request")
			}
		})
	}
}

func TestProxyHeaderParserPreservesCONNECTReadAhead(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	const prefix = "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
	const tlsBytes = "\x16\x03\x01buffered client hello"
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(client, prefix+tlsBytes); written <- err }()
	req, _, tail, err := readInitialHTTPRequest(server)
	if err != nil || req.Method != http.MethodConnect {
		t.Fatalf("CONNECT parse failed: %v", err)
	}
	got := make([]byte, len(tlsBytes))
	if _, err := io.ReadFull(tail, got); err != nil || string(got) != tlsBytes {
		t.Fatalf("CONNECT read-ahead = %q, %v", got, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}
