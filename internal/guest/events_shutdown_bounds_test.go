//go:build linux || darwin

package guest

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// An inherited event writer can remain open after the holder itself exits.
// The test owns the writer process directly, so cleanup needs no reported PID.
func TestGuestShutdownBoundsAnInheritedEventWriter(t *testing.T) {
	g, _ := shutdownBoundsInit(t, nil)
	ours, theirs, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	keepInheritedEventWriter(t, theirs)
	hp := exitedShutdownHolder(t, ours)
	g.holder = hp
	go g.passEvents(ours, hp)
	assertBoundedGuestEnd(t, g, hp)
}

// Filling the real ctl receive window must not strand passEvents in Write
// while end waits for the same forwarder to finish.
func TestGuestShutdownBoundsAFullControlWindow(t *testing.T) {
	full := make(chan struct{})
	g, guestCtl := shutdownBoundsInit(t, full)
	w := &shutdownCtlWrite{Stream: guestCtl, entered: make(chan struct{}), returned: make(chan struct{})}
	g.ctl = proto.NewWriter(w)
	ours, theirs, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = theirs.Close() })
	hp := exitedShutdownHolder(t, ours)
	g.holder = hp
	go g.passEvents(ours, hp)
	payload, err := json.Marshal(proto.CtlEvent{
		Event: proto.EventStatus, Session: "abcd2345", State: "working",
		Reason: strings.Repeat("x", 4*shutdownWindow),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload)+1 > maxEvent {
		t.Fatal("the test event exceeds the holder's legal frame size")
	}
	frame := binary.LittleEndian.AppendUint32(nil, uint32(1+len(payload)))
	frame = append(frame, byte(proto.TEvent))
	frame = append(frame, payload...)
	if _, err := theirs.Write(frame); err != nil {
		t.Fatal(err)
	}
	select {
	case <-full:
	case <-time.After(5 * time.Second):
		t.Fatal("the actual ctl receive window did not fill")
	}
	select {
	case <-w.entered:
	default:
		t.Fatal("passEvents did not enter the actual ctl stream write")
	}
	select {
	case <-w.returned:
		t.Fatal("ctl stream write returned despite its unread full window")
	default:
	}
	assertBoundedGuestEnd(t, g, hp)
	select {
	case <-w.returned:
	default:
		t.Fatal("guest end returned while ctl event forwarding was still blocked")
	}
}

const shutdownWindow = 1024

// shutdownLinkRead observes complete wire frames without reading any bytes
// from the supervisor's ctl stream or returning its receive credit.
type shutdownLinkRead struct {
	net.Conn
	trace *io.PipeWriter
}

func (c *shutdownLinkRead) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		_, _ = c.trace.Write(p[:n])
	}
	return n, err
}

type shutdownCtlWrite struct {
	*link.Stream
	entered, returned chan struct{}
	once              sync.Once
}

func (w *shutdownCtlWrite) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	n, err := w.Stream.Write(p)
	close(w.returned)
	return n, err
}

func shutdownBoundsInit(t *testing.T, full chan struct{}) (*Init, *link.Stream) {
	t.Helper()
	a, peer, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.FileConn(peer)
	_ = peer.Close()
	if err != nil {
		t.Fatal(err)
	}
	supervisorConn := a
	if full != nil {
		r, w := io.Pipe()
		supervisorConn = &shutdownLinkRead{Conn: a, trace: w}
		t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
		go observeShutdownWindow(r, full)
	}
	opt := link.Options{Window: shutdownWindow}
	sup := link.New(supervisorConn, link.Supervisor, opt)
	g := &Init{sess: link.New(b, link.Guest, opt), log: slog.New(slog.DiscardHandler), done: make(chan struct{})}
	t.Cleanup(func() {
		if g.holder != nil {
			// Direct cleanup also releases a deliberately broken closeEvents
			// mutant, without signalling any process not owned by this test.
			_ = g.holder.events.Close()
		}
		_ = g.sess.Close()
		_ = sup.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sup.Open(ctx, link.KindCtl, nil); err != nil {
		t.Fatal(err)
	}
	st, err := g.sess.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.ctlSt, g.ctl = st, proto.NewWriter(st)
	return g, st
}

func observeShutdownWindow(r io.Reader, full chan struct{}) {
	read := bufio.NewReader(r)
	total := 0
	for {
		frame, err := proto.Read(read)
		if err != nil {
			return
		}
		if frame.Type == proto.TStreamData && len(frame.Payload) > 4 {
			total += len(frame.Payload) - 4
			if total >= shutdownWindow && full != nil {
				close(full)
				full = nil
			}
		}
	}
}

func exitedShutdownHolder(t *testing.T, events net.Conn) *holderProc {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	return &holderProc{id: "abcd2345", cmd: cmd, done: done, eventsDone: make(chan struct{}), events: events}
}

func assertBoundedGuestEnd(t *testing.T, g *Init, hp *holderProc) {
	t.Helper()
	started := make(chan struct{})
	ended := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		close(started)
		g.end()
		ended <- time.Since(start)
	}()
	t.Cleanup(func() {
		_ = hp.events.Close()
		_ = g.sess.Close()
		select {
		case <-g.Done():
		case <-time.After(2 * time.Second):
			t.Error("guest shutdown did not join even after test resource cleanup")
		}
	})
	<-started
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var took time.Duration
	select {
	case took = <-ended:
	case <-timer.C:
		select {
		case took = <-ended:
		default:
			t.Fatal("guest shutdown exceeded one second after the holder's bound")
		}
	}
	if took >= time.Second {
		t.Fatalf("guest shutdown took %s after its holder had already exited", took)
	}
	t.Logf("guest shutdown returned in %s after the holder exited", took)
	select {
	case <-hp.eventsDone:
	default:
		t.Fatal("guest returned without joining holder event forwarding")
	}
	select {
	case <-g.sess.Done():
	default:
		t.Fatal("guest returned without closing its link")
	}
	var p [1]byte
	if err := hp.events.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := hp.events.Read(p[:]); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("event descriptor after shutdown = %v, want closed", err)
	}
}

func keepInheritedEventWriter(t *testing.T, writer *os.File) {
	t.Helper()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseR, releaseW, err := os.Pipe()
	if err != nil {
		_ = readyR.Close()
		_ = readyW.Close()
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "printf 'ready\\n' >&4; IFS= read -r finish <&5")
	cmd.ExtraFiles = []*os.File{writer, readyW, releaseR}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	_ = readyW.Close()
	_ = releaseR.Close()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = releaseW.Close()
		_ = readyR.Close()
	})
	if err := readyR.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(readyR).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("inherited writer readiness = %q, %v", line, err)
	}
}
