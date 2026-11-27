package guest

import (
	"bytes"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// heldEndWriter holds a complete holder end at the last forwarding step.
// Earlier status frames pass normally. This proves that the process's exit
// and completion of its event forwarding are separate facts.
type heldEndWriter struct {
	atEnd     chan struct{}
	release   chan struct{}
	linkDone  <-chan struct{}
	once      sync.Once
	forwarded atomic.Bool
}

func (w *heldEndWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"event":"ended"`)) {
		w.once.Do(func() { close(w.atEnd) })
		<-w.release
		select {
		case <-w.linkDone:
			return 0, net.ErrClosed
		default:
		}
		w.forwarded.Store(true)
	}
	return len(p), nil
}

func TestGuestShutdownWaitsForTheHoldersBufferedEnd(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "ge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	a, b := net.Pipe()
	g := New(b, root, slog.New(slog.DiscardHandler))
	sup := link.New(a, link.Supervisor, link.Options{})
	w := &heldEndWriter{atEnd: make(chan struct{}), release: make(chan struct{}), linkDone: g.sess.Done()}
	g.ctl = proto.NewWriter(w)
	var release sync.Once
	t.Cleanup(func() {
		release.Do(func() { close(w.release) })
		_ = sup.Close()
		go g.end()
		select {
		case <-g.Done():
		case <-time.After(15 * time.Second):
			t.Error("guest shutdown did not finish")
		}
	})
	exit := filepath.Join(root, "exit-now")
	_, err = g.start(proto.CtlStartArgs{
		Session: "evtq2345", Agent: "shell", Cwd: root, Cols: 80, Rows: 24,
		Argv: []string{"/bin/sh", "-c", "while [ ! -f '" + exit + "' ]; do sleep 0.01; done; exit 7"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=" + host.GuestHome, "TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	hp := g.holder
	g.mu.Unlock()
	if err := os.WriteFile(exit, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.atEnd:
	case <-time.After(10 * time.Second):
		t.Fatal("holder did not forward its terminal event")
	}
	go g.end()
	select {
	case <-hp.done:
	case <-time.After(10 * time.Second):
		t.Fatal("holder did not stop while its event forwarding was held")
	}
	// The terminal event remains held after the process was reaped. The
	// guest must keep the control stream and its own lifetime open.
	select {
	case <-g.sess.Done():
		t.Fatal("guest link closed before its holder's buffered end was forwarded")
	case <-time.After(100 * time.Millisecond):
	}
	release.Do(func() { close(w.release) })
	select {
	case <-g.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("guest did not close after its buffered event drained")
	}
	if !w.forwarded.Load() {
		t.Fatal("guest stopped without forwarding its holder's end")
	}
}
