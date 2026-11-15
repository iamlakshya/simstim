package client

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/link"
)

// These are owned in-memory transport IO oracles, not SSH, actor-heap or
// accepted-Source evidence. Production construction still requires a stamp.
type inputCarrierTestProc struct{ net.Conn }

func (p *inputCarrierTestProc) Stdin() io.WriteCloser { return p.Conn }
func (p *inputCarrierTestProc) Stdout() io.Reader     { return p.Conn }
func (*inputCarrierTestProc) Stderr() string          { return "" }
func (*inputCarrierTestProc) Wait() error             { return nil }
func (p *inputCarrierTestProc) Kill()                 { _ = p.Conn.Close() }

type inputCarrierWriteGate struct {
	net.Conn
	armed                            atomic.Bool
	entered, allow, aborted, written chan struct{}
	once, stop, finish               sync.Once
}

func (c *inputCarrierWriteGate) Write(data []byte) (int, error) {
	gated := c.armed.Load()
	if gated {
		c.once.Do(func() { close(c.entered) })
		select {
		case <-c.allow:
		case <-c.aborted:
			return 0, net.ErrClosed
		}
	}
	n, err := c.Conn.Write(data)
	if gated {
		c.finish.Do(func() { close(c.written) })
	}
	return n, err
}
func (c *inputCarrierWriteGate) Close() error {
	c.stop.Do(func() { close(c.aborted) })
	return c.Conn.Close()
}

type inputFlushContext struct {
	context.Context
	waiting chan struct{}
}

func (c *inputFlushContext) Done() <-chan struct{} {
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func TestInputRawLinkFlushWaitsForActualOwnedTransportWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	local, peer := net.Pipe()
	gate := &inputCarrierWriteGate{Conn: local, entered: make(chan struct{}), allow: make(chan struct{}), aborted: make(chan struct{}), written: make(chan struct{})}
	raw := &Conn{execReader: bufio.NewReader(local), stdin: gate, proc: &inputCarrierTestProc{local}, done: make(chan struct{})}
	adapter := &inputRawCarrier{conn: raw, input: local, output: gate}
	client := link.ContinueInputs(adapter, raw.execReader, link.Guest, false)
	server := link.ContinueInputs(peer, bufio.NewReader(peer), link.Supervisor, false)
	defer server.Close()
	defer client.Close()
	put, err := client.Open(ctx, link.KindPut, inputtransfer.Header{Purpose: "inputs"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gate.armed.Store(true)
	if _, err := put.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("actual writer was not reached")
	}
	probe := &inputFlushContext{Context: ctx, waiting: make(chan struct{}, 1)}
	finished := make(chan error, 1)
	go func() { finished <- put.Flush(probe) }()
	select {
	case <-probe.waiting:
	case err := <-finished:
		t.Fatal("Flush returned while physical writer was held", err)
	case <-ctx.Done():
		t.Fatal("Flush never reached its actual wait")
	}
	select {
	case <-gate.written:
		t.Fatal("physical write finished before its release")
	default:
	}
	close(gate.allow)
	var payload [7]byte
	if _, err := io.ReadFull(accepted, payload[:]); err != nil || string(payload[:]) != "payload" {
		t.Fatal("payload changed", err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Flush did not join the physical write")
	}
	select {
	case <-gate.written:
	default:
		t.Fatal("Flush certified a queue rather than actual transport completion")
	}
}

func TestInputRawCloseCannotManufactureReaderCompletion(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	raw := &Conn{execReader: bufio.NewReader(local), stdin: local, proc: &inputCarrierTestProc{local}, done: make(chan struct{}), execReadDone: make(chan struct{})}
	adapter := &inputRawCarrier{conn: raw, input: local, output: local}
	returned := make(chan error, 1)
	go func() { var byte [1]byte; _, err := adapter.Read(byte[:]); returned <- err }()
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-raw.execReadDone:
		t.Fatal("Close manufactured a raw-reader join")
	default:
	}
	select {
	case err := <-returned:
		if !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	case <-t.Context().Done():
		t.Fatal("owned raw read was not interrupted")
	}
	raw.execReadFinished()
	select {
	case <-raw.execReadDone:
	default:
		t.Fatal("actual reader owner did not publish its joined completion")
	}
}
