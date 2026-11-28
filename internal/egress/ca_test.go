package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionCAUniquePerStart(t *testing.T) {
	ca1, err := NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA 1: %v", err)
	}
	defer ca1.Close()

	ca2, err := NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA 2: %v", err)
	}
	defer ca2.Close()

	pem1, pem2 := ca1.CertPEM(), ca2.CertPEM()
	if pem1 == "" || pem2 == "" {
		t.Fatal("expected non-empty CA PEM")
	}
	if pem1 == pem2 {
		t.Fatal("two sessions produced identical CA certificates")
	}
	if strings.Contains(pem1, "PRIVATE KEY") || strings.Contains(pem2, "PRIVATE KEY") {
		t.Fatal("CertPEM must never contain PRIVATE KEY")
	}
	blk, rest := pem.Decode([]byte(pem1))
	if blk == nil || blk.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("unexpected PEM block: type=%v rest=%d", blk, len(rest))
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("session CA cert must have IsCA=true")
	}
}

func TestSessionCALeafVerificationCachingAndClose(t *testing.T) {
	ca, err := NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA: %v", err)
	}
	otherCA, err := NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA other: %v", err)
	}
	defer otherCA.Close()

	leaf1, err := ca.LeafForHost("Registry.NPMJS.org.")
	if err != nil {
		t.Fatalf("LeafForHost domain: %v", err)
	}
	leaf2, err := ca.LeafForHost("registry.npmjs.org")
	if err != nil {
		t.Fatalf("LeafForHost cached: %v", err)
	}
	if leaf1 != leaf2 {
		t.Fatal("expected cached leaf certificate for normalized host")
	}

	if _, err := leaf1.Leaf.Verify(x509.VerifyOptions{
		DNSName: "registry.npmjs.org",
		Roots:   ca.CertPool(),
	}); err != nil {
		t.Fatalf("leaf verify against session CA: %v", err)
	}
	if _, err := leaf1.Leaf.Verify(x509.VerifyOptions{
		DNSName: "registry.npmjs.org",
		Roots:   otherCA.CertPool(),
	}); err == nil {
		t.Fatal("leaf from CA 1 unexpectedly verified against CA 2")
	}

	ipLeaf, err := ca.LeafForHost("198.51.100.42")
	if err != nil {
		t.Fatalf("LeafForHost IP: %v", err)
	}
	if _, err := ipLeaf.Leaf.Verify(x509.VerifyOptions{
		DNSName: "198.51.100.42",
		Roots:   ca.CertPool(),
	}); err != nil {
		t.Fatalf("IP leaf verify: %v", err)
	}

	ca.Close()
	if _, err := ca.LeafForHost("example.com"); err == nil {
		t.Fatal("expected LeafForHost to fail after Close")
	}
}

func TestSessionCATLSConfigSNIALPNAndECH(t *testing.T) {
	ca, err := NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA: %v", err)
	}
	defer ca.Close()

	t.Run("matching SNI negotiates h2", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		dl := time.Now().Add(5 * time.Second)
		_ = c1.SetDeadline(dl)
		_ = c2.SetDeadline(dl)

		srvErr := make(chan error, 1)
		go func() {
			s := tls.Server(c2, ca.TLSConfigForHost("registry.npmjs.org", true))
			srvErr <- s.Handshake()
		}()

		cli := tls.Client(c1, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: "registry.npmjs.org",
			RootCAs:    ca.CertPool(),
			NextProtos: []string{"h2", "http/1.1"},
		})
		if err := cli.Handshake(); err != nil {
			t.Fatalf("client handshake: %v", err)
		}
		if got := cli.ConnectionState().NegotiatedProtocol; got != "h2" {
			t.Fatalf("ALPN = %q, want h2", got)
		}
		if err := <-srvErr; err != nil {
			t.Fatalf("server handshake: %v", err)
		}
	})

	t.Run("SNI mismatch refused", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		dl := time.Now().Add(5 * time.Second)
		_ = c1.SetDeadline(dl)
		_ = c2.SetDeadline(dl)

		srvErr := make(chan error, 1)
		go func() {
			s := tls.Server(c2, ca.TLSConfigForHost("registry.npmjs.org", true))
			srvErr <- s.Handshake()
		}()

		cli := tls.Client(c1, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: "evil.example",
			RootCAs:    ca.CertPool(),
		})
		if err := cli.Handshake(); err == nil {
			t.Fatal("expected SNI mismatch handshake to fail")
		}
		if err := <-srvErr; err == nil {
			t.Fatal("expected server error on SNI mismatch")
		}
	})

	t.Run("ECH refused", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		dl := time.Now().Add(5 * time.Second)
		_ = c1.SetDeadline(dl)
		_ = c2.SetDeadline(dl)

		srvErr := make(chan error, 1)
		go func() {
			s := tls.Server(c2, ca.TLSConfigForHost("registry.npmjs.org", true))
			srvErr <- s.Handshake()
		}()

		rawCH := buildTestClientHello(t, "registry.npmjs.org", extECHOuterExtensions, []byte{0x02, 0x00, 0x00})
		if _, err := c1.Write(rawCH); err != nil {
			t.Fatalf("write raw ECH ClientHello: %v", err)
		}
		_ = c1.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = c1.Read(make([]byte, 64))
		if err := <-srvErr; err == nil || (!strings.Contains(err.Error(), "ECH") && !strings.Contains(err.Error(), "encrypted_client_hello")) {
			t.Fatalf("server err = %v, want ECH refusal", err)
		}
	})
}

type spyWriteConn struct {
	net.Conn
	mu  sync.Mutex
	out bytes.Buffer
}

func (s *spyWriteConn) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.out.Write(p)
	s.mu.Unlock()
	return s.Conn.Write(p)
}

func (s *spyWriteConn) Written() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.out.Bytes())
}

func TestRunOnConnCACertNeverTransmitsPrivateKey(t *testing.T) {
	runOnce := func(id string) (certPEM string, fd3Wire []byte) {
		supConn, egRaw := net.Pipe()
		defer supConn.Close()
		defer egRaw.Close()
		dl := time.Now().Add(10 * time.Second)
		_ = supConn.SetDeadline(dl)
		_ = egRaw.SetDeadline(dl)

		egSpy := &spyWriteConn{Conn: egRaw}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- RunOnConn(ctx, id, egSpy, nil)
		}()

		cl, err := NewClient(ctx, supConn, StartArgs{Session: id}, nil)
		if err != nil {
			t.Fatalf("NewClient(%s): %v", id, err)
		}
		certPEM = cl.CACertPEM()
		if _, err := cl.Status(ctx); err != nil {
			t.Fatalf("Status: %v", err)
		}
		_ = cl.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("RunOnConn(%s) did not exit within timeout", id)
		}
		return certPEM, egSpy.Written()
	}

	pem1, wire1 := runOnce("sess-a")
	pem2, wire2 := runOnce("sess-b")

	if pem1 == "" || pem2 == "" {
		t.Fatal("expected CA cert PEM from init")
	}
	if pem1 == pem2 {
		t.Fatal("two egress starts returned the same CA certificate")
	}
	if !strings.Contains(pem1, "BEGIN CERTIFICATE") {
		t.Fatalf("unexpected PEM: %q", pem1)
	}
	for name, b := range map[string][]byte{"wire1": wire1, "wire2": wire2} {
		if bytes.Contains(bytes.ToUpper(b), []byte("PRIVATE KEY")) {
			t.Fatalf("%s on fd 3 contained PRIVATE KEY", name)
		}
	}
}
