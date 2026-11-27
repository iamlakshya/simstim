package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/iamlakshya/simstim/internal/brand"
	cl "github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/keys"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// closesSoon waits up to within until the supervisor has closed the guest's
// stream st, as a write of nothing says, then closes the guest's end, as the
// guest init does.
func closesSoon(t *testing.T, st *link.Stream, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := st.Write(nil); err != nil {
			_ = st.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the supervisor kept the stream of a client that went for %s", within)
}

// guestFrames reads the frames that reach the guest on st, until its end.
func guestFrames(st *link.Stream) []proto.Frame {
	r := bufio.NewReader(st)
	var out []proto.Frame
	for {
		f, err := proto.Read(r)
		if err != nil {
			return out
		}
		out = append(out, f)
	}
}

// A paste larger than the client's queue, in one input frame, reaches the
// guest whole and in order, with the frames after it, when the guest reads
// slowly: AGENTS.md rule 2.
func TestABigPasteReachesTheGuestWhole(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	conn, _, st := attachTo(t, h)
	paste := bytes.Repeat([]byte("0123456789abcdef"), (keys.MaxPaste+4096)/16+1)
	paste = append(append([]byte("\x1b[200~"), paste...), "\x1b[201~"...)
	w := proto.NewWriter(conn)
	sent := [][]byte{paste, []byte("after"), bytes.Repeat([]byte("y"), maxQueued/2)}
	go func() {
		for _, p := range sent {
			_ = w.Write(proto.TInput, p)
		}
	}()
	time.Sleep(500 * time.Millisecond) // a guest that is slow, but alive
	r := bufio.NewReader(st)
	for i, want := range sent {
		f, err := proto.Read(r)
		if err != nil || f.Type != proto.TInput || !bytes.Equal(f.Payload, want) {
			t.Fatalf("input frame %d of %d bytes reached the guest as %v of %d bytes, %v", i, len(want), f.Type, len(f.Payload), err)
		}
	}
	conn.Close()
}

// A client that goes while its reader waits for room in its queue, because
// the guest takes none of its input, still frees its stream: the watch of
// its end closes the connection, within drainWait.
func TestAClientThatGoesWhileItsInputWaitsFreesItsStream(t *testing.T) {
	old := drainWait
	drainWait = 200 * time.Millisecond
	t.Cleanup(func() { drainWait = old })
	h := hostileStart(t)
	h.answerStart(t)
	base := runtime.NumGoroutine()
	chunk := make([]byte, 256<<10)
	var wg sync.WaitGroup
	for range 6 {
		conn, _, st := attachTo(t, h)
		_ = conn.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
		w := proto.NewWriter(conn)
		// 3 MiB: past the stream's credit, its buffer and the queue.
		var werr error
		for range 12 {
			if err := w.Write(proto.TInput, chunk); err != nil {
				werr = err
				break
			}
		}
		if werr == nil {
			t.Fatal("3 MiB of input did not fill the queue")
		}
		conn.Close()
		wg.Go(func() { closesSoon(t, st, 15*time.Second) })
	}
	wg.Wait()
	conn, _, _ := attachTo(t, h)
	conn.Close()
	waitFor(t, "the goroutines of the clients that went", func() bool {
		return runtime.NumGoroutine() <= base+8
	})
}

// Input and a detach that a client sends just before it closes reach the
// guest, every time: the frames that wait pass before the stream closes.
func TestInputBeforeAClientsEndArrives(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	for try := range 50 {
		conn, _, st := attachTo(t, h)
		w := proto.NewWriter(conn)
		for range 50 {
			_ = w.Write(proto.TInput, []byte("k"))
		}
		_ = w.Write(proto.TDetach, nil)
		conn.Close()
		var input, detach int
		for _, f := range guestFrames(st) {
			switch f.Type {
			case proto.TInput:
				input += len(f.Payload)
			case proto.TDetach:
				detach++
			default:
			}
		}
		_ = st.Close()
		if input != 50 || detach != 1 {
			t.Fatalf("try %d: the guest got %d of 50 input bytes and %d detach", try, input, detach)
		}
	}
}

// partialOutput writes the head of an output frame of n bytes, and ten
// bytes of it, to st: the guest stalls in the frame.
func partialOutput(t *testing.T, st *link.Stream, n int) {
	t.Helper()
	b := make([]byte, 5, 15)
	binary.LittleEndian.PutUint32(b[:4], uint32(n+1))
	b[4] = byte(proto.TOutput)
	b = append(b, "0123456789"...)
	if _, err := st.Write(b); err != nil {
		t.Fatal(err)
	}
}

// A guest that stalls in the middle of a frame holds up neither the
// supervisor's answers to the client nor the end of a client that goes.
// More such clients than the link has streams still attach.
func TestAPartialFrameStrandsNoClient(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	var wg sync.WaitGroup
	for i := range 40 {
		conn, r, st := attachTo(t, h)
		partialOutput(t, st, 1000)
		w := proto.NewWriter(conn)
		_ = w.WriteJSON(proto.TRequest, proto.Request{ID: 7, Op: "info"})
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			f, err := proto.Read(r)
			if err != nil {
				t.Fatalf("client %d: no answer to info while the guest stalls in a frame: %v", i, err)
			}
			var resp proto.Response
			if f.Type == proto.TResponse && json.Unmarshal(f.Payload, &resp) == nil && resp.ID == 7 {
				break
			}
		}
		conn.Close()
		wg.Go(func() { closesSoon(t, st, 20*time.Second) })
	}
	wg.Wait()
	conn, _, _ := attachTo(t, h)
	conn.Close()
}

// A frame that the guest does not finish within frameStall closes the
// connection, after an error that says the sandbox sent a frame that may
// not pass. No byte of the frame reaches the client.
func TestAStalledFrameEndsTheConnection(t *testing.T) {
	old := frameStall
	frameStall = 300 * time.Millisecond
	t.Cleanup(func() { frameStall = old })
	h := hostileStart(t)
	h.answerStart(t)
	for _, write := range []func(*link.Stream){
		func(st *link.Stream) { partialOutput(t, st, 1000) },
		func(st *link.Stream) { _, _ = st.Write([]byte{10, 0}) },
	} {
		_, r, st := attachTo(t, h)
		write(st)
		f, err := proto.Read(r)
		var pe proto.Error
		if err != nil || f.Type != proto.TError || json.Unmarshal(f.Payload, &pe) != nil || pe.Code != "guest_frame" {
			t.Fatalf("after a stalled frame the client got %v %q, %v", f.Type, f.Payload, err)
		}
		if _, err := proto.Read(r); !errors.Is(err, io.EOF) {
			t.Errorf("the connection lasts after a stalled frame: %v", err)
		}
		_ = st.Close()
	}
}

// A guest that closes its holder stream while its ctl stream and the link
// stay up never makes a real client say that the session stopped: the
// attach ends with the guest's error, through the bridge, and the session
// runs on.
func TestAGuestCloseIsNoStopForARealClient(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	t.Setenv(brand.Env("TEST_CLIPBOARD_TEXT"), filepath.Join(t.TempDir(), "no-clipboard"))
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tty.Close(); ptmx.Close() })
	_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 24})
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()
	type end struct {
		res cl.AttachResult
		err error
	}
	done := make(chan end, 1)
	go func() {
		res, err := cl.Attach(context.Background(), cl.AttachOptions{Host: bridgeHost(t), Session: h.id,
			Settings: cl.DefaultSettings(), State: &cl.AttachState{}, In: tty, Out: tty})
		done <- end{res, err}
	}()
	var st *link.Stream
	select {
	case st = <-h.g.holders:
	case e := <-done:
		t.Fatalf("attach ended before holder stream opened: %+v, %v", e.res, e.err)
	case <-time.After(20 * time.Second):
		t.Fatal("no holder stream")
	}
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.TAttached, proto.Attached{Session: h.id, Mode: "replay"})
	_ = gw.Write(proto.TOutput, proto.EncodeOutput(0, []byte("HERE")))
	time.Sleep(300 * time.Millisecond)
	_ = st.Close()
	var e end
	select {
	case e = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the attach did not end")
	}
	var ue *cl.UserError
	if e.res.Ended != nil || !errors.As(e.err, &ue) || ue.Code != "guest_closed" {
		t.Errorf("after the guest closed its stream, the attach = %+v, %v; want the guest's error", e.res, e.err)
	}
	if _, err := host.HolderRequest(h.id, "info", nil, 3*time.Second); err != nil {
		t.Errorf("the session does not run on: %v", err)
	}
}

// The record of a boot is saved only once the session's socket listens:
// New dials the socket as soon as it reads the record (host.waitBoot).
func TestTheBootRecordComesOnceTheSocketListens(t *testing.T) {
	g := &hostile{disk: fixtureDiskResourcesV1(t), ctls: make(chan *link.Stream, 1), holders: make(chan *link.Stream, 64), done: make(chan struct{})}
	dir := testFolders(t)
	const id = "lstn2345"
	t0 := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "box", CreatedAt: t0, Cwd: dir,
		Agent: host.AgentInfo{Kind: "claude", Argv: []string{"/bin/sh"}}, Term: host.TermInfo{Cols: 80, Rows: 24},
		Status: host.Status{State: host.StateStarting, Since: t0},
		Wall:   &host.WallRecord{Level: "none", Driver: "fake", At: t0}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	old, oldFake := sandboxFor, testFakeSandbox
	sandboxFor, testFakeSandbox = func(*host.Manifest) (sandbox, error) {
		time.Sleep(300 * time.Millisecond)
		return g, nil
	}, true
	t.Cleanup(func() { sandboxFor, testFakeSandbox = old, oldFake })
	bad := make(chan string, 1)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if mm, err := host.LoadManifest(id); err == nil && mm.Life != nil {
				if c, err := net.Dial("unix", paths.Socket(id)); err != nil {
					bad <- err.Error()
				} else {
					c.Close()
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()
	select {
	case <-g.ctls:
	case <-time.After(10 * time.Second):
		t.Fatal("no ctl stream")
	}
	close(stop)
	select {
	case err := <-bad:
		t.Errorf("the boot's record came before the socket listened: %s", err)
	default:
	}
	_ = g.Kill()
	select {
	case <-ended:
	case <-time.After(20 * time.Second):
		t.Error("the supervisor did not stop")
	}
}
