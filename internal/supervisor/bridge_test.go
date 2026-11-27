package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/brand"
	cl "github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A client's way to this host for the real client: the real bridge,
// host.RunBridge, in this process over OS pipes, as hosttest's transport has
// it (hosttest imports this package, so its tests cannot use it).
type bridgeTransport struct {
	mu   sync.Mutex
	open map[*bridgeProc]bool
	down error
}

func (b *bridgeTransport) ReachHint() string { return "check the test" }

func (b *bridgeTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("the test's transport runs only the bridge")
}

func (b *bridgeTransport) Drop() int {
	b.mu.Lock()
	procs := make([]*bridgeProc, 0, len(b.open))
	for p := range b.open {
		procs = append(procs, p)
	}
	b.mu.Unlock()
	for _, p := range procs {
		p.Kill()
	}
	return len(procs)
}

func (b *bridgeTransport) SetDown(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = err
}

func (b *bridgeTransport) Start(ctx context.Context, _ string) (cl.Proc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down != nil {
		return nil, b.down
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open bridge stdin: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, fmt.Errorf("open bridge stdout: %w", err)
	}
	readDone := make(chan struct{})
	var readOnce sync.Once
	out := &bridgeOutPipe{f: outR, onDone: func() { readOnce.Do(func() { close(readDone) }) }}
	p := &bridgeProc{in: inW, out: out, done: make(chan struct{}), readDone: readDone}
	if b.open == nil {
		b.open = map[*bridgeProc]bool{}
	}
	b.open[p] = true
	stop := context.AfterFunc(ctx, p.Kill)
	go func() { //nolint:contextcheck // the bridge runs until its input closes, as the real one
		_ = host.RunBridge(inR, outW)
		stop()
		_ = outW.Close()
		_ = inR.Close()
		b.mu.Lock()
		delete(b.open, p)
		b.mu.Unlock()
		close(p.done)
		time.AfterFunc(200*time.Millisecond, func() { _ = out.Close() })
	}()
	return p, nil
}

type bridgeOutPipe struct {
	f      *os.File
	mu     sync.Mutex
	closed bool
	onDone func()
}

func (o *bridgeOutPipe) Read(p []byte) (int, error) {
	n, err := o.f.Read(p)
	if err != nil {
		_ = o.Close()
	}
	return n, err
}

func (o *bridgeOutPipe) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	err := o.f.Close()
	o.onDone()
	return err
}

type bridgeProc struct {
	in       *os.File
	out      *bridgeOutPipe
	done     chan struct{}
	readDone chan struct{}
	killed   atomic.Bool
}

var errBridgeKilled = errors.New("bridge killed")

func (p *bridgeProc) Stdin() io.WriteCloser { return p.in }
func (p *bridgeProc) Stdout() io.Reader     { return p.out }
func (p *bridgeProc) Stderr() string        { return "" }

func (p *bridgeProc) Wait() error {
	<-p.done
	if p.killed.Load() {
		return errBridgeKilled
	}
	select {
	case <-p.readDone:
	case <-time.After(200 * time.Millisecond):
		_ = p.out.Close()
	}
	if p.killed.Load() {
		return errBridgeKilled
	}
	return nil
}

func (p *bridgeProc) Kill() {
	p.killed.Store(true)
	_ = p.in.Close()
	_ = p.out.Close()
}

// bridgeMachine is always running, and reached through a bridgeTransport.
type bridgeMachine struct{ t *bridgeTransport }

func (bridgeMachine) Status(context.Context) (string, error) { return cl.MachineRunning, nil }
func (bridgeMachine) Start(context.Context) error            { return nil }
func (bridgeMachine) Stop(context.Context) error             { return nil }
func (bridgeMachine) Destroy(context.Context) error          { return nil }
func (m bridgeMachine) Transport(context.Context, bool) (cl.Transport, error) {
	return m.t, nil
}

var (
	bridgeOnce     sync.Once
	bridgeMachines sync.Map
	bridgeSeq      atomic.Uint64
)

// bridgeHost is a client's host that reaches this process's sessions
// through the real bridge.
func bridgeHost(t *testing.T) cl.Host {
	t.Helper()
	h, _ := newBridgeHost(t)
	return h
}

func newBridgeHost(t *testing.T) (cl.Host, *bridgeTransport) {
	t.Helper()
	const provider = "supervisortest"
	bridgeOnce.Do(func() {
		cgroup := fmt.Sprintf("0::/user.slice/user-%d.slice/session-1.scope\n", os.Getuid())
		host.SelfCgroup = func() ([]byte, error) { return []byte(cgroup), nil }
		host.CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[1]", nil }
		cl.RegisterProvider(provider, func(ch cl.Host) (cl.Lifecycle, error) {
			v, ok := bridgeMachines.Load(ch.Machine.ID)
			if !ok {
				return nil, fmt.Errorf("no test machine %q", ch.Machine.ID)
			}
			return bridgeMachine{t: v.(*bridgeTransport)}, nil
		})
	})
	tr := &bridgeTransport{}
	id := fmt.Sprintf("box-%d", bridgeSeq.Add(1))
	bridgeMachines.Store(id, tr)
	t.Cleanup(func() {
		tr.Drop()
		bridgeMachines.Delete(id)
	})
	h := cl.Host{Name: "box", Machine: &cl.Machine{Provider: provider, ID: id}}
	if err := cl.SaveHosts(context.Background(), []cl.Host{h}); err != nil {
		t.Fatal(err)
	}
	return h, tr
}

// answerHolderAttach plays a wedged guest holder on the next holder stream:
// it answers hello and attach, then stops reading input and writes no output.
func answerHolderAttach(t *testing.T, h *hostileRun, live []byte, early ...<-chan error) *link.Stream {
	t.Helper()
	var errCh <-chan error
	if len(early) > 0 {
		errCh = early[0]
	}
	var st *link.Stream
	select {
	case st = <-h.g.holders:
	case err := <-errCh:
		t.Fatalf("attach ended before holder stream opened: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("no holder stream opened")
	}
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	if _, err := proto.Read(gr); err != nil {
		t.Fatalf("read holder hello: %v", err)
	}
	if err := gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"}); err != nil {
		t.Fatalf("write holder hello: %v", err)
	}
	if _, err := proto.Read(gr); err != nil {
		t.Fatalf("read attach: %v", err)
	}
	if err := gw.WriteJSON(proto.TAttached, proto.Attached{Session: h.id, Mode: "replay"}); err != nil {
		t.Fatalf("write attached: %v", err)
	}
	if len(live) > 0 {
		_ = gw.Write(proto.TOutput, proto.EncodeOutput(0, live))
	}
	return st
}

// A client attached through the real bridge to a guest that stops reading
// input and writes no output neither freezes the bridge on detach nor strands
// its holder stream when the transport drops while backpressured.
func TestAClientThatGoesBehindTheBridgeFreesItsStream(t *testing.T) {
	old := drainWait
	drainWait = 200 * time.Millisecond
	t.Cleanup(func() { drainWait = old })

	h := hostileStart(t)
	h.answerStart(t)
	t.Setenv(brand.Env("TEST_CLIPBOARD_TEXT"), filepath.Join(t.TempDir(), "no-clipboard"))

	chunk := bytes.Repeat([]byte("a"), 32<<10)

	var part1WG sync.WaitGroup
	// Part 1: while the holder socket is backpressured, the bridge's async
	// queue still lets the loop read TDetach and answer requests on the same
	// connection.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := cl.Dial(ctx, bridgeHost(t), false)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		stCh := make(chan *link.Stream, 1)
		go func() {
			stCh <- answerHolderAttach(t, h, nil)
		}()
		if err := conn.SendJSON(proto.TAttach, proto.Attach{Session: h.id, Cols: 80, Rows: 24, LastOffset: -1}); err != nil {
			t.Fatal(err)
		}
		st := <-stCh
		select {
		case <-conn.Stream():
		case <-time.After(5 * time.Second):
			t.Fatal("no TAttached")
		}
		// 48 * 32 KiB = 1536 KiB fills the 256 KiB link window, the 1 MiB
		// supervisor queue and the Unix socket buffer on both Linux and macOS,
		// while fitting inside the bridge's 512 KiB queue.
		for range 48 {
			if err := conn.Send(proto.TInput, chunk); err != nil {
				t.Fatal(err)
			}
		}
		if err := conn.Send(proto.TDetach, nil); err != nil {
			t.Fatal(err)
		}
		callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
		defer callCancel()
		if _, err := conn.Sessions(callCtx); err != nil {
			t.Fatalf("bridge froze on holder write instead of answering ls after detach: %v", err)
		}
		part1WG.Go(func() { closesSoon(t, st, 15*time.Second) })
	}()

	// Part 2: repeat client.Attach maxHolderStreams + 4 (36) times, filling
	// the entire input path until backpressured and dropping the transport.
	bh, tr := newBridgeHost(t)
	var streamWG sync.WaitGroup
	const total = 36
	for i := range total {
		if i == 24 {
			// Let the first wave finish its 5 s link reset linger in parallel
			// so all 36 attaches cross maxHolderStreams (32) cleanly.
			part1WG.Wait()
			streamWG.Wait()
		}
		tr.SetDown(nil)
		ptmx, tty, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = term.MakeRaw(int(tty.Fd()))
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 24})
		var outBuf bytes.Buffer
		var outMu sync.Mutex
		var outDone chan struct{}
		if i == total-1 {
			outDone = make(chan struct{})
			go func() {
				defer close(outDone)
				buf := make([]byte, 8<<10)
				for {
					n, rerr := ptmx.Read(buf)
					if n > 0 {
						outMu.Lock()
						outBuf.Write(buf[:n])
						outMu.Unlock()
					}
					if rerr != nil {
						return
					}
				}
			}()
		}

		ctx, cancel := context.WithCancel(context.Background())
		attachDone := make(chan error, 1)
		go func() {
			_, aerr := cl.Attach(ctx, cl.AttachOptions{
				Host: bh, Session: h.id,
				Settings: cl.DefaultSettings(), State: &cl.AttachState{},
				In: tty, Out: tty,
			})
			attachDone <- aerr
		}()

		var want []byte
		if i == total-1 {
			want = []byte("ATTACH-36-OK")
		}
		st := answerHolderAttach(t, h, want, attachDone)

		var writerDone chan struct{}
		if i == total-1 {
			waitFor(t, "attach #36 output", func() bool {
				outMu.Lock()
				defer outMu.Unlock()
				return bytes.Contains(outBuf.Bytes(), want)
			})
			tr.SetDown(errors.New("done"))
			cancel()
			tr.Drop()
		} else {
			// Write past link window + supervisor 1 MiB + Unix socket + bridge
			// 512 KiB + OS pipe until ptmx blocks on backpressure, then drop
			// the client transport.
			writerDone = make(chan struct{})
			stopWrite := make(chan struct{})
			var wrote atomic.Int64
			go func() {
				defer close(writerDone)
				for range 96 {
					select {
					case <-stopWrite:
						return
					default:
					}
					n, werr := ptmx.Write(chunk)
					wrote.Add(int64(n))
					if werr != nil {
						return
					}
				}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				before := wrote.Load()
				time.Sleep(15 * time.Millisecond)
				if before > (1600<<10) && wrote.Load() == before {
					break
				}
			}
			close(stopWrite)
			tr.SetDown(errors.New("transport dropped"))
			cancel()
			tr.Drop()
		}

		select {
		case aerr := <-attachDone:
			if aerr != nil && !errors.Is(aerr, context.Canceled) && !errors.Is(aerr, cl.ErrClosed) {
				t.Fatalf("attach #%d = %v", i+1, aerr)
			}
		case <-time.After(10 * time.Second):
			_ = tty.Close()
			t.Fatalf("attach #%d hung after transport closed", i+1)
		}
		streamWG.Go(func() { closesSoon(t, st, 15*time.Second) })
		if writerDone != nil {
			cr, cerr := cancelreader.NewReader(tty)
			if cerr == nil {
				drainDone := make(chan struct{})
				go func() {
					defer close(drainDone)
					_, _ = io.Copy(io.Discard, cr)
				}()
				<-writerDone
				cr.Cancel()
				<-drainDone
				_ = cr.Close()
			} else {
				<-writerDone
			}
			_ = tty.Close()
			_ = ptmx.Close()
		} else {
			_ = tty.Close()
			if outDone != nil {
				<-outDone
			}
			_ = ptmx.Close()
		}
	}
	part1WG.Wait()
	streamWG.Wait()
}
