package agentbridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type relayProbe struct {
	net.Conn
	mu         sync.Mutex
	readMax    int
	writeMax   int
	readCalls  int
	buffers    map[*byte]bool
	readerFrom bool
	writerTo   bool
}

func (p *relayProbe) Read(b []byte) (int, error) {
	p.mu.Lock()
	p.readMax = max(p.readMax, len(b))
	p.readCalls++
	if len(b) > 0 {
		if p.buffers == nil {
			p.buffers = map[*byte]bool{}
		}
		p.buffers[&b[0]] = true
	}
	p.mu.Unlock()
	return p.Conn.Read(b)
}

func (p *relayProbe) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.writeMax = max(p.writeMax, len(b))
	p.mu.Unlock()
	return p.Conn.Write(b)
}

func (p *relayProbe) CloseWrite() error {
	if half, ok := p.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return net.ErrClosed
}

func (p *relayProbe) ReadFrom(io.Reader) (int64, error) {
	p.mu.Lock()
	p.readerFrom = true
	p.mu.Unlock()
	return 0, errors.New("unexpected ReaderFrom fast path")
}

func (p *relayProbe) WriteTo(io.Writer) (int64, error) {
	p.mu.Lock()
	p.writerTo = true
	p.mu.Unlock()
	return 0, errors.New("unexpected WriterTo fast path")
}

func (p *relayProbe) checkBuffers(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readMax != 32768 || p.writeMax == 0 || p.writeMax > 32768 || len(p.buffers) != 1 || p.readCalls < 2 || p.readerFrom || p.writerTo {
		t.Fatalf("relay buffers or copy path changed: read=%d write=%d buffers=%d calls=%d ReaderFrom=%v WriterTo=%v",
			p.readMax, p.writeMax, len(p.buffers), p.readCalls, p.readerFrom, p.writerTo)
	}
}

func TestAgentWriteBytesCompletesPartialWritesAndRejectsNoProgress(t *testing.T) {
	want := []byte("a bounded agent response")
	var got bytes.Buffer
	calls := 0
	if err := writeBytes(writerFunc(func(p []byte) (int, error) {
		calls++
		if calls > len(want)+1 {
			return 0, errors.New("partial-write loop made no progress")
		}
		return got.Write(p[:min(3, len(p))])
	}), want); err != nil || !bytes.Equal(got.Bytes(), want) || calls < 2 {
		t.Fatalf("partial writes lost bytes: %q, calls=%d, err=%v", got.Bytes(), calls, err)
	}
	failed := errors.New("controlled relay write failure")
	for _, result := range []struct {
		n   int
		err error
	}{{0, nil}, {100, nil}, {2, failed}} {
		calls := 0
		err := writeBytes(writerFunc(func([]byte) (int, error) {
			calls++
			if calls > 1 {
				return 0, failed
			}
			return result.n, result.err
		}), want)
		expected := failed
		if result.err == nil {
			expected = io.ErrShortWrite
		}
		if !errors.Is(err, expected) || calls != 1 {
			t.Fatalf("invalid relay write result: %v, calls=%d", err, calls)
		}
	}
}

type eofAgentReader struct{ calls int }

func (r *eofAgentReader) Read(p []byte) (int, error) {
	r.calls++
	return copy(p, "final bytes"), io.EOF
}

type halfTestConn struct {
	net.Conn
	reader io.Reader
	writer io.Writer
	half   func() error
}

func (c halfTestConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c halfTestConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c halfTestConn) CloseWrite() error           { return c.half() }

func TestCopyHalfWritesFinalReadBytesBeforeItsHalfClose(t *testing.T) {
	reader := &eofAgentReader{}
	var got bytes.Buffer
	var order []string
	dst := halfTestConn{
		writer: writerFunc(func(p []byte) (int, error) { order = append(order, "write"); return got.Write(p) }),
		half:   func() error { order = append(order, "half-close"); return nil },
	}
	if err := copyHalf(dst, halfTestConn{reader: reader}); err != nil {
		t.Fatal(err)
	}
	if got.String() != "final bytes" || reader.calls != 1 || len(order) != 2 || order[0] != "write" || order[1] != "half-close" {
		t.Fatalf("EOF discarded final bytes or closed too early: %q, reads=%d, order=%v", got.String(), reader.calls, order)
	}
}

type agentReaderFunc func([]byte) (int, error)

func (r agentReaderFunc) Read(p []byte) (int, error) { return r(p) }

func TestCopyHalfPreservesReadAndHalfCloseFailures(t *testing.T) {
	failed := errors.New("controlled relay failure")
	writes, closes := 0, 0
	dst := halfTestConn{
		writer: writerFunc(func(p []byte) (int, error) { writes++; return len(p), nil }),
		half:   func() error { closes++; return failed },
	}
	if err := copyHalf(dst, halfTestConn{reader: agentReaderFunc(func([]byte) (int, error) { return 0, failed })}); !errors.Is(err, failed) || writes != 0 || closes != 0 {
		t.Fatalf("read failure was hidden or half-closed: %v, writes=%d closes=%d", err, writes, closes)
	}
	if err := copyHalf(dst, halfTestConn{reader: &eofAgentReader{}}); !errors.Is(err, failed) || writes != 1 || closes != 1 {
		t.Fatalf("half-close failure was hidden: %v, writes=%d closes=%d", err, writes, closes)
	}
}

func TestRelayFailureInterruptsTheOtherDirectionAndClosesOwnedEndpoints(t *testing.T) {
	a, clientA := net.Pipe()
	b, clientB := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close(); clientA.Close(); clientB.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Relay(ctx, a, b) }()
	// net.Pipe cannot half-close. Its EOF must fail the relay and interrupt
	// the opposite direction, which is still blocked reading from a.
	_ = clientB.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("relay lost its half-close failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay failure did not interrupt the opposite direction")
	}
	_ = clientA.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := clientA.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("the failed relay retained an owned endpoint: %v", err)
	}
}
