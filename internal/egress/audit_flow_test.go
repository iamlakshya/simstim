package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLiveAuditScrubsAcceptedAndRejectedRequestTargets(t *testing.T) {
	dialer := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			conn, peer := net.Pipe()
			t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
			go func() {
				defer peer.Close()
				req, err := http.ReadRequest(bufio.NewReader(peer))
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				_ = req.Body.Close()
				_, _ = io.WriteString(peer, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}()
			return conn, nil
		},
	}
	env := startTestServer(t, dialer)
	env.call(t, CtlInit, InitArgs{
		SessionID: "audit2345", Agent: "shell",
		Policy:  pathTestPolicy(t, api.PresetLocked, "example.com:80/"),
		Secrets: map[string]string{"KEY": "sk-live-secret-987654"},
	})
	for i, tc := range []struct {
		target string
		status int
		path   string
	}{
		{"http://example.com/hook/sk-live-%e2%80%8bsecret-987654?not_logged", 200, "/hook/[KEY]"},
		{"http://example.com/hook/%252573k-live-secret-987654", 403, "/hook/[KEY]"},
		{"http://example.com/%25ZZ/%2573k-live-secret-987654", 403, "/%ZZ/[KEY]"},
		{"HTTP://example.com/v1/chat", 200, "/v1/chat"},
		{"HTTP://user:password@example.com/v1/chat", 400, "/v1/chat"},
	} {
		request := "GET " + tc.target + " HTTP/1.1\r\nHost: example.com\r\n\r\n"
		if status := pathHTTPExchange(t, env, request); status != tc.status {
			t.Fatalf("flow %d status = %d, want %d", i, status, tc.status)
		}
		audits := env.waitAudits(t, i+1)
		if got := audits[i]; got.Path != tc.path || got.Status != tc.status {
			t.Fatalf("flow %d emitted %+v, want status=%d path=%q", i, got, tc.status, tc.path)
		}
	}
}
