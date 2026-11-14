package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnedHTTPClientConnRejectsMissingOrChangedScopeBeforeDial(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	var lookups, dials atomic.Int64
	server.dialer.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return nil, errors.New("unexpected lookup")
	}
	server.dialer.DialTCP = func(context.Context, netip.AddrPort) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected dial")
	}
	for _, change := range []string{"missing-context", "different-context", "different-server", "scheme", "authority", "host", "method", "path", "query", "ended"} {
		t.Run(change, func(t *testing.T) {
			scope := &originRequest{s: server, scheme: "https", request: Request{Host: "example.com", Port: 443, Method: "GET", Path: "/v1"},
				ctx: t.Context(), permit: server.currentEngine().NewPermit()}
			ctx := context.WithValue(t.Context(), originRequestKey{}, scope)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com:443/v1", nil)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing-context":
				req = req.WithContext(t.Context())
			case "different-context":
				req = req.WithContext(context.WithValue(t.Context(), originRequestKey{}, &originRequest{}))
			case "different-server":
				scope.s = new(Server)
			case "scheme":
				req.URL.Scheme = "http"
			case "authority":
				req.URL.Host = "other.example.com:443"
			case "host":
				req.Host = "other.example.com:443"
			case "method":
				req.Method = http.MethodPost
			case "path":
				req.URL.Path = "/other"
			case "query":
				req.URL.RawQuery = "changed=after-admission"
			case "ended":
				scope.closed = true
			}
			if response, err := (requestHTTPTransport{server: server, scope: scope}).RoundTrip(req); err == nil || response != nil {
				t.Fatal("unowned or changed outgoing request acquired an origin connection")
			}
			if lookups.Load() != 0 || dials.Load() != 0 {
				t.Fatal("scope refusal happened after resolver or dial effects")
			}
		})
	}
}

func TestClosedCheckedHTTPConnRefusesTLSCleanupWriteWithoutReenteringStop(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	var cancelled atomic.Int64
	scope := &originRequest{s: server, ctx: t.Context(), closed: true, cancel: func() { cancelled.Add(1) }}
	checked := &checkedHTTPConn{Conn: conn, scope: scope}
	n, err := checked.Write([]byte("TLS-cleanup-probe"))
	if n != 0 || !errors.Is(err, net.ErrClosed) || cancelled.Load() != 0 {
		t.Fatal("closed origin write re-entered teardown or attempted live policy admission")
	}
}

type ownedWriteProbeConn struct {
	net.Conn
	writes, closes atomic.Int64
}

func (c *ownedWriteProbeConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}
func (c *ownedWriteProbeConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

func TestLiveCheckedHTTPRefusalReturnsBeforeTLSWriteGateTeardown(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	probe := &ownedWriteProbeConn{Conn: conn}
	var writeGate sync.Mutex
	writeGate.Lock() // TLS retains this gate until the checked write returns.
	release := sync.OnceFunc(writeGate.Unlock)
	defer release()
	var cancelled atomic.Int64
	scope := &originRequest{s: server, ctx: t.Context(), conn: probe,
		request: Request{Host: "denied.example.com", Port: 443, Method: http.MethodGet, Path: "/denied"},
		permit:  server.currentEngine().NewPermit(), cancel: func() {
			writeGate.Lock()
			cancelled.Add(1)
			writeGate.Unlock()
		}}
	checked := &checkedHTTPConn{Conn: probe, scope: scope}
	type answer struct {
		n   int
		err error
	}
	done := make(chan answer, 1)
	go func() { n, err := checked.Write([]byte("live-refusal-probe")); done <- answer{n, err} }()
	var got answer
	blocked := false
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		blocked = true
		release() // Rescue an old-code reversion and join its exact worker.
		select {
		case got = <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("refused checked write retained its owned worker after gate release")
		}
	}
	if blocked || got.n != 0 || !errors.Is(got.err, ErrRefusedAddr) || probe.writes.Load() != 0 || probe.closes.Load() != 0 || cancelled.Load() != 0 {
		t.Fatal("live refusal wrote origin bytes or synchronously re-entered TLS-gated teardown")
	}
	release()
	scope.stopIO() // The owned outer request finaliser runs after Write returns.
	if probe.closes.Load() != 1 || cancelled.Load() != 1 {
		t.Fatal("the outer request finaliser did not close and cancel its own refused origin")
	}
}

func TestOwnedHTTPClientConnKeepsStreamingOriginUntilCompleteProxyResponse(t *testing.T) {
	for _, guest := range []int{1, 2} {
		for _, origin := range []int{1, 2} {
			t.Run(fmt.Sprintf("guest%d_origin%d", guest, origin), func(t *testing.T) {
				release := make(chan struct{})
				var once sync.Once
				finish := func() { once.Do(func() { close(release) }) }
				t.Cleanup(finish)
				f := newSecretRouteFixture(t, guest, origin, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					_, _ = io.WriteString(w, "ready|")
					if http.NewResponseController(w).Flush() != nil {
						return
					}
					select {
					case <-release:
						_, _ = io.WriteString(w, "tail")
					case <-req.Context().Done():
					}
				}))
				response := secretRouteRequest(t, f, http.MethodGet, "/allowed/stream", "Bearer "+f.sentinel())
				defer response.Body.Close()
				first := make([]byte, len("ready|"))
				if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "ready|" || f.originCloses.Load() != 0 {
					t.Fatal("connection closed at response headers or before the streaming body completed")
				}
				finish()
				last := secretRouteRead(t, response)
				if response.StatusCode != http.StatusOK || string(last) != "tail" {
					t.Fatal("owned origin lost the final streaming response bytes")
				}
				waitHTTPState(t, func() bool { return f.originCloses.Load() == 1 }, "the complete request's native origin connection close")
			})
		}
	}
}

func TestOwnedHTTPClientConnConcurrentRequestsHaveDistinctPhysicalOrigins(t *testing.T) {
	seen := make(chan int64, 2)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	f := newSecretRouteFixture(t, 2, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		physical, _ := req.Context().Value(secretRoutePhysicalOriginKey{}).(int64)
		select {
		case seen <- physical:
		case <-req.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "ready|")
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		select {
		case <-release:
			_, _ = io.WriteString(w, "end")
		case <-req.Context().Done():
		}
	}))
	peer := newSecretRouteH2Peer(t, f)
	peer.request(t, 1, "/allowed/first", f.sentinel())
	peer.request(t, 3, "/allowed/second", f.sentinel())
	physical := make(map[int64]bool)
	for range 2 {
		select {
		case id := <-seen:
			physical[id] = true
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent requests did not reach their independently checked physical origins")
		}
	}
	if len(physical) != 2 || physical[0] || f.lookups.Load() != 2 || f.dials.Load() != 2 || f.requests.Load() != 2 {
		t.Fatal("concurrent requests borrowed one physical origin or bypassed an actual checked dial")
	}
	finish()
	peer.window(t, 1, 65535)
	peer.window(t, 3, 65535)
	bodies := map[uint32][]byte{1: nil, 3: nil}
	ended := make(map[uint32]bool)
	for len(ended) != 2 {
		frame := peer.next(t)
		if frame.stream != 1 && frame.stream != 3 {
			continue
		}
		if frame.reset {
			t.Fatal("an independently owned concurrent response was reset")
		}
		bodies[frame.stream] = append(bodies[frame.stream], frame.data...)
		if frame.ended {
			ended[frame.stream] = true
		}
	}
	for _, body := range bodies {
		if string(body) != "ready|end" {
			t.Fatal("an independently owned response lost its complete bytes")
		}
	}
	waitHTTPState(t, func() bool { return f.originCloses.Load() == 2 }, "both complete native origin connection closes")
}
