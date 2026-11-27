package agentbridge

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Conn gives a dedicated, already verified process stream to link. Close
// owns only the supplied transport callback and never joins the link reader.
type Conn struct {
	in    io.Reader
	out   io.Writer
	close func() error
	once  sync.Once
	err   error
}

// NewConn retains the buffered identity reader. The callback must interrupt
// both owned pipe directions and must not call link.Session.Close recursively.
func NewConn(in io.Reader, out io.Writer, closeTransport func() error) *Conn {
	return &Conn{in: in, out: out, close: closeTransport}
}

// NewProcessConn retains owned raw stdio for one helper process. Closing
// this logical adapter does not promise to interrupt raw I/O. Its caller
// MUST exit that process after revocation and never reuse this connection.
func NewProcessConn(in io.Reader, out io.Writer) *Conn {
	return &Conn{in: in, out: out}
}

// Read receives the dedicated process's bytes, including buffered bytes.
func (c *Conn) Read(p []byte) (int, error) {
	return c.in.Read(p) //nolint:wrapcheck // io.Reader must preserve io.EOF for framing readers
}

// Write sends bytes only to the dedicated process.
func (c *Conn) Write(p []byte) (int, error) {
	return c.out.Write(p) //nolint:wrapcheck // the transport preserves its writer error contract
}

// Close invokes the owned transport callback exactly once without joining
// readers. Process connections reserve inherited descriptors until exit.
func (c *Conn) Close() error {
	c.once.Do(func() {
		if c.close != nil {
			c.err = c.close()
		}
	})
	return c.err
}

// LocalAddr identifies the logical local endpoint, not a network listener.
func (*Conn) LocalAddr() net.Addr { return address("local") }

// RemoteAddr identifies the logical remote endpoint.
func (*Conn) RemoteAddr() net.Addr { return address("remote") }

// SetDeadline reports that pipe deadlines are unavailable. Link implements
// its own hello timeout and stream deadlines and closes this owned transport.
func (*Conn) SetDeadline(time.Time) error { return os.ErrNoDeadline }

// SetReadDeadline reports that this adapter cannot set pipe read deadlines.
func (*Conn) SetReadDeadline(time.Time) error { return os.ErrNoDeadline }

// SetWriteDeadline reports that this adapter cannot set pipe write deadlines.
func (*Conn) SetWriteDeadline(time.Time) error { return os.ErrNoDeadline }

type address string

func (address) Network() string  { return "agent-stdio" }
func (a address) String() string { return string(a) }
