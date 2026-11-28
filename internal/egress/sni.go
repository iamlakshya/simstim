package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"time"
)

const (
	// RFC 9849 Encrypted Client Hello TLS extension IDs.
	extEncryptedClientHello uint16 = 0xfe0d
	extECHOuterExtensions   uint16 = 0xfd00

	// maxClientHelloBytes bounds the bytes buffered while reading a ClientHello.
	maxClientHelloBytes = 64 << 10

	defaultSNITimeout = 10 * time.Second
)

// ClientHello holds the fields inspected from a TLS ClientHello via crypto/tls.
type ClientHello struct {
	ServerName string
	HasECH     bool
	Extensions []uint16
}

var errPeekDone = errors.New("egress: client hello captured")

// PeekClientHello reads the initial TLS ClientHello from conn using the
// standard library crypto/tls parser, returning the parsed metadata and the
// exact raw bytes to replay to the upstream server. No bytes are ever written
// back to conn during the peek.
func PeekClientHello(ctx context.Context, conn net.Conn, timeout time.Duration) (ClientHello, []byte, error) {
	if timeout <= 0 {
		timeout = defaultSNITimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return ClientHello{}, nil, fmt.Errorf("egress: set TLS peek deadline: %w", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	rc := &recordingConn{Conn: conn, max: maxClientHelloBytes}
	var out ClientHello
	captured := false

	srv := tls.Server(rc, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			out = ClientHello{
				ServerName: chi.ServerName,
				Extensions: slices.Clone(chi.Extensions),
				HasECH: slices.Contains(chi.Extensions, extEncryptedClientHello) ||
					slices.Contains(chi.Extensions, extECHOuterExtensions),
			}
			captured = true
			return nil, errPeekDone
		},
	})
	err := srv.HandshakeContext(ctx)
	if !captured {
		return ClientHello{}, nil, fmt.Errorf("egress: read TLS ClientHello: %w. Try: send a valid TLS ClientHello on port 443", err)
	}
	return out, rc.buf.Bytes(), nil
}

// CheckClientHello enforces spec 7.2 on port 443: refuse ECH and verify that
// the ClientHello SNI matches the requested CONNECT host.
func CheckClientHello(ch ClientHello, targetHost string) error {
	if ch.HasECH {
		return errors.New("egress: TLS encrypted ClientHello (ECH) is refused. Try: turn off ECH in the client")
	}
	normTarget, err := normalizeHost(targetHost)
	if err != nil {
		return fmt.Errorf("egress: invalid target host %q: %w", targetHost, err)
	}
	// RFC 6066 forbids IP literals in SNI. When the user explicitly granted an
	// IP literal, require an empty SNI so a client cannot smuggle a domain name.
	if _, ipErr := netip.ParseAddr(normTarget); ipErr == nil {
		if ch.ServerName != "" {
			return fmt.Errorf("egress: TLS SNI %q does not match IP target %s. Try: connect by host name instead", ch.ServerName, normTarget)
		}
		return nil
	}
	if ch.ServerName == "" {
		return fmt.Errorf("egress: TLS ClientHello for %s has no SNI. Try: enable TLS SNI in the client", normTarget)
	}
	normSNI, err := normalizeHost(ch.ServerName)
	if err != nil || normSNI != normTarget {
		return fmt.Errorf("egress: TLS SNI %q does not match CONNECT host %q. Try: connect to the host named in SNI", ch.ServerName, normTarget)
	}
	return nil
}

// recordingConn records every byte read up to max and refuses writes so that
// tls.Server never sends an alert back to the client during PeekClientHello.
type recordingConn struct {
	net.Conn
	buf bytes.Buffer
	max int
}

func (c *recordingConn) Read(p []byte) (int, error) {
	rem := c.max - c.buf.Len()
	if rem <= 0 {
		return 0, errors.New("egress: TLS ClientHello exceeds 64 KiB limit")
	}
	if len(p) > rem {
		p = p[:rem]
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.buf.Write(p[:n])
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("egress: read client hello bytes: %w", err)
	}
	return n, err //nolint:wrapcheck // io.EOF must be returned directly to crypto/tls
}

func (c *recordingConn) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
