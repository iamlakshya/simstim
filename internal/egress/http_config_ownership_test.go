package egress

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestHTTPServersOwnMutableConfigDuringConcurrentTemplateReuse(t *testing.T) {
	// Extra capacity makes accidental ALPN appends observable even though
	// the template's original slice length would remain unchanged.
	protocols := make([]string, 4)
	protocols[0] = "custom"
	shared := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: protocols[:1], SessionTicketsDisabled: true}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 2 {
		client, server := net.Pipe()
		_ = client.Close()
		_ = server.Close()
		workers.Go(func() {
			<-start
			serveHTTPConnection(ctx, tls.Server(server, shared), shared, http.NotFoundHandler(), nil)
		})
	}
	close(start)
	workers.Wait()
	if ctx.Err() != nil {
		t.Fatal("owned HTTP servers did not finish their closed inherited streams")
	}
	if shared.PreferServerCipherSuites || shared.MinVersion != tls.VersionTLS12 || !shared.SessionTicketsDisabled || len(shared.NextProtos) != 1 {
		t.Fatal("HTTP/2 server setup mutated the caller's shared TLS template")
	}
	if protocols[0] != "custom" || protocols[1] != "" || protocols[2] != "" || protocols[3] != "" {
		t.Fatal("owned server ALPN setup borrowed writable template slice capacity")
	}
}
