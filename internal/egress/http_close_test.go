package egress

import (
	"bufio"
	"bytes"
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

type delayedResponseConn struct {
	net.Conn
	started   chan struct{}
	release   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (c *delayedResponseConn) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("checked")) {
		c.startOnce.Do(func() { close(c.started) })
		select {
		case <-c.release:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *delayedResponseConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestHTTPProxyPreservesFinalCloseResponseUnderGuestBackpressure(t *testing.T) {
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
	env.call(t, CtlInit, InitArgs{Policy: api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.example.com:80/final"}}}})
	client, server := net.Pipe()
	defer client.Close()
	gate := &delayedResponseConn{Conn: server, started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	defer gate.Close()
	done := make(chan AuditEntry, 1)
	go func() {
		done <- env.srv.ServeFlow(context.Background(), gate, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 80})
	}()
	if _, err := io.WriteString(client, "GET http://api.example.com/final HTTP/1.1\r\nHost: api.example.com\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("response body did not reach the guest forwarding boundary")
	}
	select {
	case <-gate.closed:
		t.Fatal("HTTP server closed the guest before forwarding its final response body")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate.release)
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "checked" {
		t.Fatalf("final response bytes lost under backpressure: %q, %v", body, err)
	}
	select {
	case entry := <-done:
		if entry.Status != http.StatusOK {
			t.Fatalf("final audit = %+v", entry)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed closing response kept its guest flow alive")
	}
}
