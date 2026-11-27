package link

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type shortHelloConn struct {
	net.Conn
	dataWrites int
}

func (c *shortHelloConn) Write(p []byte) (int, error) {
	if len(p) > 4 && proto.Type(p[4]) == proto.THello {
		return len(p) - 1, nil
	}
	c.dataWrites++
	return len(p), nil
}

func (*shortHelloConn) Close() error { return nil }

func TestShortHelloNeverPublishesAQueuedStream(t *testing.T) {
	conn := &shortHelloConn{}
	s, st := flushStream(t, conn)
	writeFlushBytes(t, st)
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.writeLoop() }()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a short nil-error hello did not fail the link")
	}
	if err := st.Flush(testCtx(t)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Flush after short hello = %v", err)
	}
	if conn.dataWrites != 0 {
		t.Fatalf("%d stream batches followed an incomplete hello", conn.dataWrites)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written != 0 {
		t.Fatalf("short hello advanced stream completion to %d", s.written)
	}
}
