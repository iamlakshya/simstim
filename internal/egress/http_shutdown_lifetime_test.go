package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func TestSessionStopClosesTheLocalCarrierBeforeItsClientHello(t *testing.T) {
	dialer := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			t.Error("a local carrier waiting for ClientHello dialled an origin")
			return nil, net.ErrClosed
		},
	}
	env := startTestServer(t, dialer)
	env.call(t, CtlInit, InitArgs{Policy: pathTestPolicy(t, "locked", "api.example.com/held")})
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		env.srv.ServeFlow(context.Background(), server, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 443})
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		waitHTTPProgress(t, finished, "the owned pending carrier to exit")
	})
	if _, err := io.WriteString(client, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("local CONNECT failed: %v, %v", response, err)
	}
	// No ClientHello is sent. The direct ServeFlow pipe is independent of
	// the control link, so closing that link alone cannot satisfy this test.
	if err := env.srv.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("session shutdown left a pending local TLS carrier alive")
	}
	env.srv.flows.mu.Lock()
	requests, carriers := len(env.srv.flows.flows), len(env.srv.flows.tunnels)
	env.srv.flows.mu.Unlock()
	if requests != 0 || carriers != 0 {
		t.Fatalf("session shutdown retained resources: requests=%d carriers=%d", requests, carriers)
	}
}

func TestStoppedServerRejectsALateLocalCarrierWithoutOriginIO(t *testing.T) {
	env := startTestServer(t, &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			t.Error("a stopped server resolved a new origin")
			return nil, net.ErrClosed
		},
	})
	env.call(t, CtlInit, InitArgs{Policy: pathTestPolicy(t, "locked", "api.example.com/held")})
	if err := env.srv.Close(); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		env.srv.ServeFlow(context.Background(), server, StreamHeader{Via: "explicit", Host: "api.example.com", Port: 443})
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		waitHTTPProgress(t, finished, "the owned late carrier to exit")
	})
	if _, err := io.WriteString(client, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n"); err == nil {
		response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
		var timeout net.Error
		if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatalf("stopped server admitted or stalled a new carrier: response=%v error=%v", response, err)
		}
	}
	waitHTTPProgress(t, finished, "rejected late local carrier")
	env.srv.flows.mu.Lock()
	requests, carriers := len(env.srv.flows.flows), len(env.srv.flows.tunnels)
	env.srv.flows.mu.Unlock()
	if requests != 0 || carriers != 0 {
		t.Fatalf("late admission leaked resources: requests=%d carriers=%d", requests, carriers)
	}
}
