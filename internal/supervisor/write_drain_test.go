package supervisor

import (
	"bufio"
	"bytes"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type notifyWriteError struct {
	once sync.Once
	seen chan struct{}
}

func (w *notifyWriteError) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.seen) })
	return 0, syscall.EPIPE
}

// A closed output half does not discard final input that already reached
// the supervisor. In particular, an output EPIPE must not close the input
// half while the first large frame backpressures the holder stream.
func TestOutputFailureStillDrainsCleanClientInput(t *testing.T) {
	started := make(chan *Supervisor, 1)
	old := testStarted
	testStarted = func(s *Supervisor) { started <- s }
	t.Cleanup(func() { testStarted = old })
	h := hostileStart(t)
	h.answerStart(t)
	s := <-started
	conn, _, st := attachTo(t, h)
	t.Cleanup(func() { _ = conn.Close(); _ = st.Close() })
	s.mu.Lock()
	c := s.leader
	s.mu.Unlock()
	if c == nil {
		t.Fatal("no attached client")
	}
	seen := make(chan struct{})
	c.wmu.Lock()
	c.w = proto.NewWriter(&notifyWriteError{seen: seen})
	c.wmu.Unlock()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		t.Fatal("test client is not a Unix connection")
	}
	if err := unixConn.CloseRead(); err != nil {
		t.Fatal(err)
	}
	first := bytes.Repeat([]byte("a"), proto.MaxFrame-1)
	last := bytes.Repeat([]byte("z"), 64<<10)
	sent := make(chan error, 1)
	go func() {
		w := proto.NewWriter(conn)
		for _, p := range [][]byte{first, last} {
			if err := w.Write(proto.TInput, p); err != nil {
				sent <- err
				return
			}
		}
		if err := w.Write(proto.TDetach, nil); err != nil {
			sent <- err
			return
		}
		sent <- unixConn.CloseWrite()
	}()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client's final input did not reach the supervisor")
	}
	go func() { _ = proto.NewWriter(st).Write(proto.TOutput, proto.EncodeOutput(0, []byte("late output"))) }()
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent output did not encounter the closed read half")
	}
	_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(st)
	for i, want := range [][]byte{first, last} {
		f, err := proto.Read(r)
		if err != nil || f.Type != proto.TInput || !bytes.Equal(f.Payload, want) {
			t.Fatalf("input %d: type=%v size=%d err=%v, want %d unchanged bytes", i, f.Type, len(f.Payload), err, len(want))
		}
	}
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TDetach {
		t.Fatalf("final detach: type=%v err=%v", f.Type, err)
	}
}
