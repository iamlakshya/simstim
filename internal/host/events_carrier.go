package host

import (
	"bufio"
	"errors"
	"net"
	"sync"
	"time"
)

// bridgeEventConn transfers only the actual process-owned bridge IO. It has no
// net.Pipe queue: successful writes retain the native output owner's meaning.
type bridgeEventConn struct {
	in   OwnedBridgeInput
	out  OwnedBridgeOutput
	once sync.Once
	err  error
}

func (c *bridgeEventConn) Read(p []byte) (int, error)  { return c.in.Read(p) }
func (c *bridgeEventConn) Write(p []byte) (int, error) { return c.out.Write(p) }
func (c *bridgeEventConn) Close() error {
	c.once.Do(func() { c.err = errors.Join(c.in.Cancel(), c.out.Cancel()) })
	return c.err
}
func (*bridgeEventConn) LocalAddr() net.Addr  { return bridgeEventsAddr("host") }
func (*bridgeEventConn) RemoteAddr() net.Addr { return bridgeEventsAddr("client") }
func (c *bridgeEventConn) SetDeadline(at time.Time) error {
	return errors.Join(c.SetReadDeadline(at), c.SetWriteDeadline(at))
}
func (c *bridgeEventConn) SetReadDeadline(at time.Time) error {
	if native, ok := c.in.(interface{ SetReadDeadline(time.Time) error }); ok {
		return native.SetReadDeadline(at)
	}
	if at.IsZero() {
		return nil
	}
	return errors.ErrUnsupported
}
func (c *bridgeEventConn) SetWriteDeadline(at time.Time) error {
	if native, ok := c.out.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return native.SetWriteDeadline(at)
	}
	if at.IsZero() {
		return nil
	}
	return errors.ErrUnsupported
}

type bridgeEventsAddr string

func (bridgeEventsAddr) Network() string  { return "stdio" }
func (a bridgeEventsAddr) String() string { return string(a) }

func (b *bridge) continueEventsCarrier(reader *bufio.Reader) (net.Conn, *bufio.Reader, func() error, error) {
	if reader == nil || b.ownedInput == nil || b.ownedOutput == nil {
		return nil, nil, nil, UserError("events_unavailable", "the event channel has no complete native IO owner", "Use the current complete host helper entrypoint.")
	}
	connection := &bridgeEventConn{in: b.ownedInput, out: b.ownedOutput}
	finish := func() error {
		// The caller closes/joins its Link reader and writer BEFORE this join.
		// Close only cancels native owners, so it cannot recursively join Link.
		cancelErr := connection.Close()
		return errors.Join(cancelErr, b.ownedInput.Wait(), b.ownedOutput.Wait())
	}
	return connection, reader, finish, nil
}
