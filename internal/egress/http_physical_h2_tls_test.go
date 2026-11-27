package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestPhysicalH2PreservesOriginalNativeTLSCipherRefusalBeforeAnyHandler(t *testing.T) {
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("cipher.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	guest, native := net.Pipe()
	_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
	_ = native.SetDeadline(time.Now().Add(5 * time.Second))
	ctx, cancel := context.WithCancel(t.Context())
	var handlers atomic.Int64
	served := make(chan error, 1)
	config := &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{*certificate}, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}}
	go func() {
		served <- servePhysicalHTTP2(ctx, tls.Server(native, config), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlers.Add(1) }))
	}()
	t.Cleanup(func() {
		cancel()
		_ = guest.Close()
		_ = native.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("original rejected TLS carrier did not join")
		}
	})
	client := tls.Client(guest, &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, ServerName: "cipher.example.test", RootCAs: roots, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}})
	if err := client.HandshakeContext(t.Context()); err != nil {
		t.Fatal("actual TLS fixture did not negotiate the original prohibited cipher", err)
	}
	state := client.ConnectionState()
	if !state.HandshakeComplete || state.Version != tls.VersionTLS12 || state.CipherSuite != tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA || state.NegotiatedProtocol != "h2" || len(state.VerifiedChains) == 0 {
		t.Fatal("fixture skipped the actual original verified TLS state")
	}
	written := make(chan struct{})
	go func() { _, _ = io.WriteString(client, http2.ClientPreface); close(written) }()
	frame, err := http2.NewFramer(io.Discard, client).ReadFrame()
	goaway, ok := frame.(*http2.GoAwayFrame)
	if err != nil || !ok || goaway.ErrCode != http2.ErrCodeInadequateSecurity || handlers.Load() != 0 {
		t.Fatal("above-TLS view skipped original HTTP2 TLS cipher refusal", err)
	}
	_ = client.NetConn().Close()
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("original rejected peer's pending preface write did not join")
	}
}
