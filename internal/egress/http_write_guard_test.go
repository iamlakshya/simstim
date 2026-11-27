package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestOriginWriteRechecksAnExpiredRequestWithoutWaitingForTheWatcher(t *testing.T) {
	at := time.Now()
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "api.example.com/timed", Until: at.Add(time.Hour)})
	engine, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}
	engine.now = func() time.Time { return at }
	request := Request{Host: "api.example.com", Port: 443, Method: http.MethodGet, Path: "/timed"}
	permit := engine.NewPermit()
	defer permit.Close()
	if !engine.evaluateHTTP(request, permit, evalReserve).Allow {
		t.Fatal("initial timed HTTP request was not allowed")
	}
	permit.Commit()
	server := &Server{engine: engine, dialer: &Dialer{}, flows: newFlowTracker(maxActiveFlows)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	scope := &originRequest{s: server, request: request, permit: permit, ctx: ctx, cancel: cancel, reserved: true,
		http2: true, addr: netip.MustParseAddr("198.51.100.8")}
	measured := &measuredOriginConn{Conn: conn}
	guarded := &checkedHTTPConn{Conn: measured, scope: scope}
	scope.conn, scope.measured = guarded, measured
	read := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(peer); read <- data }()
	// No expiry watcher runs in this fixture. A literal origin write must
	// evaluate the current deadline itself before passing any TLS/HTTP bytes.
	at = at.Add(2 * time.Hour)
	n, writeErr := guarded.Write([]byte("payload"))
	_ = guarded.Close()
	select {
	case data := <-read:
		if n != 0 || !errors.Is(writeErr, ErrRefusedAddr) || len(data) != 0 {
			t.Fatalf("expired request wrote %d bytes with error %v, origin got %q", n, writeErr, data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expired origin write left its owned reader blocked")
	}
}
