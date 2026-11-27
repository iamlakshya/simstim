package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTP1OnceEndWaitsForTheFinalForwardedResponse(t *testing.T) {
	dialer := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
					return
				}
				_, _ = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 7\r\nConnection: close\r\n\r\nchecked")
			}()
			return client, nil
		},
	}
	env := startTestServer(t, dialer)
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "api.example.com:80/final", Once: true, GrantID: "forward-instance"})
	env.call(t, CtlInit, InitArgs{Policy: pol})
	client, server := net.Pipe()
	defer client.Close()
	gate := &delayedResponseConn{Conn: server, started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	defer gate.Close()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	defer release()
	finished := make(chan AuditEntry, 1)
	go func() {
		finished <- env.srv.ServeFlow(context.Background(), gate, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 80})
	}()
	if _, err := io.WriteString(client, "GET http://api.example.com/final HTTP/1.1\r\nHost: api.example.com\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); _ = response.Body.Close() }()
	waitHTTPProgress(t, gate.started, "the final response at guest backpressure")
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
	select {
	case <-gate.closed:
		t.Fatal("an unrelated reload closed a response still using its own once grant")
	case <-time.After(100 * time.Millisecond):
	}
	env.mu.Lock()
	audits, ends := len(env.audits), len(env.ended)
	env.mu.Unlock()
	if audits != 0 || ends != 0 {
		t.Fatalf("audit/end preceded final forwarding: audits=%d ends=%d", audits, ends)
	}
	release()
	if body, err := io.ReadAll(response.Body); err != nil || string(body) != "checked" {
		t.Fatalf("once response lost its final bytes: %q, %v", body, err)
	}
	select {
	case entry := <-finished:
		if entry.Status != http.StatusOK {
			t.Fatalf("final once response audit: %+v", entry)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forwarded response did not release its request lifetime")
	}
	waitHTTPState(t, func() bool { env.mu.Lock(); defer env.mu.Unlock(); return len(env.ended) == 1 }, "one exact once end after forwarding")
	env.mu.Lock()
	defer env.mu.Unlock()
	if len(env.audits) != 1 || len(env.events) != 2 || env.events[0] != EventAudit || env.events[1] != EventOnceEnd || env.ended[0].GrantID != "forward-instance" {
		t.Fatalf("wrong audit order or duplicate events: audits=%+v events=%v ends=%+v", env.audits, env.events, env.ended)
	}
}
