package supervisor

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type expiredOutputConn struct{ net.Conn }

func (*expiredOutputConn) SetWriteDeadline(time.Time) error { return nil }
func (*expiredOutputConn) Write([]byte) (int, error)        { return 0, os.ErrDeadlineExceeded }
func (*expiredOutputConn) Close() error                     { return nil }

// Releasing a failed client's large frame must not wait for the holder
// stream's normal five-second close linger behind unread queued input.
func TestExpiredClientWriteDoesNotLingerOnHolderInput(t *testing.T) {
	a, b := net.Pipe()
	opt := link.Options{Window: 1024}
	sup := link.New(a, link.Supervisor, opt)
	gst := link.New(b, link.Guest, opt)
	t.Cleanup(func() { _ = sup.Close(); _ = gst.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	accepted := make(chan *link.Stream, 1)
	go func() { st, _ := gst.Accept(ctx); accepted <- st }()
	st, err := sup.Open(ctx, link.KindHolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote := <-accepted
	if remote == nil {
		t.Fatal("guest did not accept the holder stream")
	}
	defer remote.Close()
	_ = st.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	n, err := st.Write(bytes.Repeat([]byte("q"), 4*int(opt.Window)))
	if !errors.Is(err, os.ErrDeadlineExceeded) || n == 0 {
		t.Fatalf("holder input was not backpressured: n=%d err=%v", n, err)
	}
	_ = st.SetWriteDeadline(time.Time{})
	conn := &expiredOutputConn{}
	c := &client{conn: conn, w: proto.NewWriter(conn), done: make(chan struct{}), gs: st}
	returned := make(chan error, 1)
	go func() { returned <- c.write(proto.TSnapshot, []byte("snapshot")) }()
	select {
	case err := <-returned:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired client write held its relay buffer behind holder input")
	}
}
