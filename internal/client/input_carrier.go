package client

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// This direct adapter owns no pump or queue. A link Stream.Flush therefore
// observes completion of the actual accepted transport's stdin.Write.
type inputRawCarrier struct {
	conn   *Conn
	input  io.ReadCloser
	output io.WriteCloser
	once   sync.Once
	err    error
}

func newInputRawCarrier(c *Conn) (*inputRawCarrier, error) {
	if c == nil || c.agentOnly || c.execReader == nil || c.proc == nil || c.stdin == nil {
		return nil, inputCarrierError()
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return nil, err
	}
	input, ok := c.proc.Stdout().(io.ReadCloser)
	if !ok || !interruptibleInputReader(input) || !interruptibleInputWriter(c.stdin) {
		return nil, inputCarrierError()
	}
	return &inputRawCarrier{conn: c, input: input, output: c.stdin}, nil
}

func interruptibleInputReader(reader io.ReadCloser) bool {
	switch value := reader.(type) {
	case net.Conn:
		return true // net.Conn.Close explicitly interrupts blocked reads/writes.
	case *io.PipeReader:
		return true // This concrete owned pipe supplies the same Close contract.
	case *os.File:
		return value != os.Stdin && value != os.Stdout && value != os.Stderr && value.SetReadDeadline(time.Time{}) == nil
	default:
		return false
	}
}

func interruptibleInputWriter(writer io.WriteCloser) bool {
	switch value := writer.(type) {
	case net.Conn:
		return true
	case *io.PipeWriter:
		return true
	case *os.File:
		return value != os.Stdin && value != os.Stdout && value != os.Stderr && value.SetWriteDeadline(time.Time{}) == nil
	default:
		return false
	}
}

func inputCarrierError() error {
	return &UserError{Msg: "the owned input transport cannot interrupt its actual reader and writer safely",
		Hint: "Use a host transport with owned interruptible process pipes.", Code: "input_transport", Kind: KindRefused}
}

// Read consumes only the already retained raw response reader.
func (c *inputRawCarrier) Read(data []byte) (int, error) {
	return c.conn.execReader.Read(data) //nolint:wrapcheck // Preserve framing EOF.
}

// Write reaches the actual owned transport writer directly, without a queue.
func (c *inputRawCarrier) Write(data []byte) (int, error) {
	return c.output.Write(data) //nolint:wrapcheck // Preserve the physical writer result.
}

// Close interrupts owned IO and process only. The mux owner joins its actual
// reader/writer before calling execReadFinished; Close cannot invent that join.
func (c *inputRawCarrier) Close() error {
	c.once.Do(func() {
		inputErr, outputErr := c.input.Close(), c.output.Close()
		c.conn.kill()
		if errors.Is(inputErr, net.ErrClosed) || errors.Is(inputErr, os.ErrClosed) {
			inputErr = nil
		}
		if errors.Is(outputErr, net.ErrClosed) || errors.Is(outputErr, os.ErrClosed) {
			outputErr = nil
		}
		c.err = errors.Join(inputErr, outputErr)
		c.conn.fail(ErrClosed)
	})
	return c.err
}

func (*inputRawCarrier) LocalAddr() net.Addr  { return inputRawAddress("local") }
func (*inputRawCarrier) RemoteAddr() net.Addr { return inputRawAddress("remote") }

// Deadline support is reported from the actual owned directions. Link's own
// stream deadlines do not turn an unsupported transport deadline into success.
func (c *inputRawCarrier) SetReadDeadline(at time.Time) error {
	if source, ok := c.input.(interface{ SetReadDeadline(time.Time) error }); ok {
		return source.SetReadDeadline(at)
	}
	return os.ErrNoDeadline
}
func (c *inputRawCarrier) SetWriteDeadline(at time.Time) error {
	if target, ok := c.output.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return target.SetWriteDeadline(at)
	}
	return os.ErrNoDeadline
}
func (c *inputRawCarrier) SetDeadline(at time.Time) error {
	return errors.Join(c.SetReadDeadline(at), c.SetWriteDeadline(at))
}

type inputRawAddress string

func (inputRawAddress) Network() string  { return "stdio" }
func (a inputRawAddress) String() string { return string(a) }
