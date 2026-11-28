package sshserve

import (
	"context"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }

type stdioConn struct {
	r io.Reader
	w io.WriteCloser
}

func (c *stdioConn) Read(p []byte) (int, error)       { return c.r.Read(p) }  //nolint:wrapcheck // net.Conn pass-through over stdio
func (c *stdioConn) Write(p []byte) (int, error)      { return c.w.Write(p) } //nolint:wrapcheck // net.Conn pass-through over stdio
func (c *stdioConn) Close() error                     { return c.w.Close() }  //nolint:wrapcheck // net.Conn pass-through over stdio
func (c *stdioConn) LocalAddr() net.Addr              { return stdioAddr{} }
func (c *stdioConn) RemoteAddr() net.Addr             { return stdioAddr{} }
func (c *stdioConn) SetDeadline(time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(time.Time) error { return nil }

// ServeStdio serves SSH over os.Stdin and os.Stdout for OpenSSH ProxyCommand.
func ServeStdio(ctx context.Context, hostKey ssh.Signer, backend Backend) error {
	return Serve(ctx, &stdioConn{r: os.Stdin, w: os.Stdout}, hostKey, backend)
}
