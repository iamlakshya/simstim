package sshserve

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type stdioAddress string

func (a stdioAddress) Network() string { return "stdio" }
func (a stdioAddress) String() string  { return string(a) }

// stdioConnection owns only this invocation's inherited SSH transport. Close
// interrupts its actual descriptors; it never opens a listening socket.
type stdioConnection struct {
	input  *os.File
	output *os.File
	once   sync.Once
	err    error
}

func (c *stdioConnection) Read(data []byte) (int, error)  { return c.input.Read(data) }
func (c *stdioConnection) Write(data []byte) (int, error) { return c.output.Write(data) }
func (c *stdioConnection) LocalAddr() net.Addr            { return stdioAddress("local-ssh") }
func (c *stdioConnection) RemoteAddr() net.Addr           { return stdioAddress("stdio-peer") }
func (c *stdioConnection) Close() error {
	c.once.Do(func() { c.err = errors.Join(c.input.Close(), c.output.Close()) })
	return c.err
}
func (c *stdioConnection) SetDeadline(at time.Time) error {
	return errors.Join(c.SetReadDeadline(at), c.SetWriteDeadline(at))
}
func (c *stdioConnection) SetReadDeadline(at time.Time) error {
	err := c.input.SetReadDeadline(at)
	if errors.Is(err, os.ErrNoDeadline) {
		return nil // actual cancellation still closes the owned pipe
	}
	return err
}
func (c *stdioConnection) SetWriteDeadline(at time.Time) error {
	err := c.output.SetWriteDeadline(at)
	if errors.Is(err, os.ErrNoDeadline) {
		return nil
	}
	return err
}

var _ net.Conn = (*stdioConnection)(nil)
var _ io.ReadWriteCloser = (*stdioConnection)(nil)
