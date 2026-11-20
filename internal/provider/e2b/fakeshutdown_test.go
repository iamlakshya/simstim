package e2b

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// An install cancelled mid-upload: the client's SIGKILL reaches the shell,
// but not its `cat`, which still holds the stream's stderr and waits for more
// input. This hung TestInstallOverEnvd when its context ran out under load.
func TestFakeEnvdShutsDownAfterACancelledUpload(t *testing.T) {
	e, envd := envdFor(t, false)
	file := filepath.Join(t.TempDir(), "upload")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gaveUp := make(chan error, 1)
	go func() {
		// The shape of the install's upload: cat is not the last command, so
		// the shell runs it as a child.
		_, err := e.Run(ctx, fmt.Sprintf("set -e; cat > %q; echo __INSTALLED__", file), zeros{})
		gaveUp <- err
	}()
	waitFor(t, "the upload's start", func() bool {
		st, err := os.Stat(file)
		return err == nil && st.Size() > 0
	})
	shutsDownAfterCancel(t, envd, cancel, gaveUp)
}

// A cancelled command whose child reads no input: only a kill of the whole
// process group ends the child.
func TestFakeEnvdShutsDownAfterACancelledShellChild(t *testing.T) {
	e, envd := envdFor(t, false)
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := e.Start(ctx, fmt.Sprintf("sleep 120 & echo $! > %q; wait", pidFile))
	if err != nil {
		t.Fatal(err)
	}
	gaveUp := make(chan error, 1)
	go func() { _, _ = io.Copy(io.Discard, p.Stdout()); gaveUp <- p.Wait() }()
	waitFor(t, "the child's start", func() bool {
		b, err := os.ReadFile(pidFile)
		return err == nil && strings.HasSuffix(string(b), "\n")
	})
	shutsDownAfterCancel(t, envd, cancel, gaveUp)
}

// A cancelled bridge that waits for its input for ever.
func TestFakeEnvdShutsDownAfterACancelledBridge(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.bridge = func(in io.Reader, out io.Writer, _ string) error {
		_, err := io.Copy(out, in)
		return err
	}
	envd.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := e.Start(ctx, "/nowhere/helper bridge")
	if err != nil {
		t.Fatal(err)
	}
	gaveUp := make(chan error, 1)
	go func() { _, _ = io.Copy(io.Discard, p.Stdout()); gaveUp <- p.Wait() }()
	shutsDownAfterCancel(t, envd, cancel, gaveUp)
}

// Commands that resist: the shutdown ends each of them, and each child in
// their group, while SendInput calls wait on a full stdin.
func TestFakeEnvdShutsDownStubbornCommands(t *testing.T) {
	for name, script := range map[string]string{
		"ignores TERM":    "trap '' TERM HUP INT; sleep 120 & wait",
		"grandchildren":   "sh -c 'sleep 120 & sleep 121 & wait' & wait",
		"many children":   "for i in 1 2 3 4 5; do sleep 12$i & done; wait",
		"background loop": "while :; do sleep 1; done & wait",
	} {
		t.Run(name, func(t *testing.T) {
			e, envd := envdFor(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p, err := e.Start(ctx, script)
			if err != nil {
				t.Fatal(err)
			}
			go func() { _, _ = io.Copy(p.Stdin(), io.LimitReader(zeros{}, 256<<10)) }()
			gaveUp := make(chan error, 1)
			go func() { _, _ = io.Copy(io.Discard, p.Stdout()); gaveUp <- p.Wait() }()
			waitFor(t, "a SendInput call", func() bool { return strings.Contains(strings.Join(envd.Paths(), " "), "SendInput") })
			shutsDownAfterCancel(t, envd, cancel, gaveUp)
		})
	}
}

// A dropped stream: the handler returned, and the command runs on with its
// output drainers registered. A child in the group ends at the shutdown, while
// an escaped child that holds an output descriptor makes the shutdown fail in
// its time and name the dropped stream.
func TestFakeEnvdShutsDownAfterADroppedStream(t *testing.T) {
	t.Run("child in group", func(t *testing.T) {
		e, envd := envdFor(t, false)
		envd.mu.Lock()
		envd.drop = true
		envd.mu.Unlock()
		p, err := e.Start(context.Background(), "sleep 120 & wait")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Kill)
		_, _ = io.ReadAll(p.Stdout())
		if err := envd.shutdown(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("escaped child holding output", checkDroppedStreamEscapedChild)
}

// A bridge that ends at once, while input still comes: a SendInput call then
// fails instead of waiting for a reader, and the shutdown is quick.
func TestFakeEnvdShutsDownAfterABridgeEnds(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.bridge = func(io.Reader, io.Writer, string) error { return nil }
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "/nowhere/helper bridge")
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		_, err := p.Stdin().Write(make([]byte, 3*chunk))
		if cerr := p.Stdin().Close(); err == nil {
			err = cerr
		}
		sent <- err
	}()
	_, _ = io.ReadAll(p.Stdout())
	_ = p.Wait()
	select {
	case <-sent:
	case <-time.After(30 * time.Second):
		t.Fatal("the input to a bridge that ended did not fail in 30s")
	}
	if err := envd.shutdown(); err != nil {
		t.Fatal(err)
	}
}

// A request with part of its body, which comes once the shutdown has closed
// the connections: there the old shutdown still took connections, and the
// request waited for the rest of its body for ever. Now the fake stops taking
// connections as it closes them, so the request is refused.
func TestFakeEnvdShutdownTakesNoLateRequest(t *testing.T) {
	_, envd := envdFor(t, false)
	addr := strings.TrimPrefix(envd.srv.URL, "http://")
	refused := false
	envd.afterClose = func() {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			refused = true
			return
		}
		t.Cleanup(func() { _ = c.Close() })
		_, _ = io.WriteString(c, "POST /process.Process/StreamInput HTTP/1.1\r\nHost: "+addr+"\r\n"+
			"Content-Type: "+contentStream+"\r\nConnect-Protocol-Version: 1\r\nKeepalive-Ping-Interval: 50\r\n"+
			"X-Access-Token: "+testToken+"\r\nE2b-Sandbox-Id: "+testID+"\r\n"+
			"Authorization: Basic dXNlcjo=\r\nContent-Length: 1000\r\n\r\npart of a body")
		waitFor(t, "the late request's handler", func() bool { return slices.Contains(envd.run.left(), "the StreamInput handler") })
	}
	if err := envd.shutdown(); err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("the shutdown took a request after it closed the connections")
	}
}

// StreamInput that waits on a full stdin when the fake shuts down: the
// shutdown ends the command and its stdin, and the write that then fails is
// no fault of the client's.
func TestFakeEnvdShutsDownWhileStreamInputWaits(t *testing.T) {
	e, envd := envdFor(t, true)
	p, err := e.Start(context.Background(), "exec sleep 120")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Kill)
	go func() { _, _ = io.Copy(io.Discard, p.Stdout()) }()
	// sleep never reads its stdin: fill it, so the fake's next write waits.
	envd.mu.Lock()
	in := envd.stdin[p.(*proc).pid].(*os.File)
	envd.mu.Unlock()
	buf := make([]byte, 64<<10)
	for {
		_ = in.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
		if _, err := in.Write(buf); err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal(err)
			}
			break
		}
	}
	_ = in.SetWriteDeadline(time.Time{})
	go func() { _, _ = p.Stdin().Write([]byte("more input")) }()
	waitFor(t, "a write that waits on the full stdin", func() bool {
		envd.mu.Lock()
		defer envd.mu.Unlock()
		return envd.writing > 0
	})
	if err := envd.shutdown(); err != nil {
		t.Fatal(err)
	}
}

// The shutdown ends the fake's context before a request's own: a request
// that fails in between is no fault of the client's.
func TestFakeEnvdRequestErrorDuringShutdown(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	f := &fakeEnvd{t: t, ctx: ctx, stop: stop}
	stop()
	r := httptest.NewRequest(http.MethodPost, "/process.Process/StreamInput", nil) // its context lives
	f.requestError(r, errors.New("write |1: file already closed"))
}

// The command gets only its stdin, stdout and stderr, as from envd: not the
// wrapper's report, orders or stderr, files 3 to 5.
func TestFakeEnvdKeepsTheWrappersFilesFromTheCommand(t *testing.T) {
	e, _ := envdFor(t, false)
	out, err := e.Run(context.Background(), `for fd in 3 4 5; do if { true >&$fd; } 2>/dev/null; then echo "open $fd"; fi; done; echo end`, nil)
	if err != nil || string(out) != "end\n" {
		t.Fatalf("the command's files: %q, %v", out, err)
	}
}

// A SIGKILL acts as in envd: the command's stdin and output close, and its
// stream ends with the end event, though a child of the command still holds
// them. So 10 killed commands leave no stream that runs.
func TestFakeEnvdKillEndsTheStream(t *testing.T) {
	e, envd := envdFor(t, false)
	dir := t.TempDir()
	for i := range 10 {
		// The shell's child holds the stream's stdout and stderr.
		pidFile := filepath.Join(dir, fmt.Sprint(i))
		p, err := e.Start(context.Background(), fmt.Sprintf("sleep 120 & echo $! > %q; wait", pidFile))
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the child's start", func() bool {
			b, err := os.ReadFile(pidFile)
			return err == nil && bytes.HasSuffix(b, []byte("\n"))
		})
		p.Kill()
	}
	waitFor(t, "the end of every killed command's stream", func() bool {
		for _, name := range envd.run.left() {
			if strings.HasPrefix(name, "the stream of") {
				return false
			}
		}
		return true
	})
}

// Paused output readers expose a signal that closes pipes before they drain.
// A surviving child must neither lose the queued bytes nor hold the stream.
func TestFakeEnvdKillKeepsBufferedOutput(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "natural_exit_with_child"
		if live {
			name = "live_command_with_child"
		}
		t.Run(name, func(t *testing.T) {
			e, f := envdFor(t, false)
			gate := make(chan struct{})
			f.mu.Lock()
			f.pipeGate = gate
			f.mu.Unlock()
			ready := filepath.Join(t.TempDir(), "ready")
			script := fmt.Sprintf("sleep 120 & printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2; echo ready > %q; exit 7", ready)
			if live {
				script = fmt.Sprintf("sleep 120 & printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2; echo ready > %q; cat > /dev/null", ready)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			p, err := e.Start(ctx, script)
			if err != nil {
				t.Fatal(err)
			}
			pid := p.(*proc).pid
			waitFor(t, "both output markers", func() bool { _, err := os.Stat(ready); return err == nil })
			if !live {
				f.mu.Lock()
				cmd := f.procs[pid]
				f.mu.Unlock()
				select {
				case <-cmd.ended:
				case <-ctx.Done():
					t.Fatal("the command did not exit before the signal")
				}
			}
			if err := e.unary(ctx, "SendSignal", signalRequest{Process: selector{Pid: pid}, Signal: "SIGNAL_SIGKILL"}); err != nil {
				t.Fatal(err)
			}
			close(gate)
			out, err := io.ReadAll(p.Stdout())
			if err != nil {
				t.Fatal(err)
			}
			werr := p.Wait()
			wantExit := "exit status 7"
			if live {
				wantExit = "exit status -1"
			}
			if string(out) != "out-1\nout-2\n" || p.Stderr() != "err-1\nerr-2\n" || werr == nil || werr.Error() != wantExit {
				t.Fatalf("stdout=%q, stderr=%q, Wait=%v, want ordered output and %s", out, p.Stderr(), werr, wantExit)
			}
		})
	}

	t.Run("active_reader_and_spamming_child", func(t *testing.T) {
		e, f := envdFor(t, false)
		ready := filepath.Join(t.TempDir(), "ready")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		p, err := e.Start(ctx, fmt.Sprintf("printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2; (while :; do printf x; done) 2>/dev/null & echo ready > %q; cat > /dev/null", ready))
		if err != nil {
			t.Fatal(err)
		}
		type result struct {
			out []byte
			err error
		}
		read := make(chan result, 1)
		go func() { b, err := io.ReadAll(p.Stdout()); read <- result{b, err} }()
		waitFor(t, "the writing child", func() bool { _, err := os.Stat(ready); return err == nil })
		f.mu.Lock()
		cmd := f.procs[p.(*proc).pid]
		f.mu.Unlock()
		waitFor(t, "the active stderr reader", func() bool {
			cmd.stderr.mu.Lock()
			defer cmd.stderr.mu.Unlock()
			return cmd.stderr.reading
		})
		if err := e.unary(ctx, "SendSignal", signalRequest{Process: selector{Pid: p.(*proc).pid}, Signal: "SIGNAL_SIGKILL"}); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-read:
			if got.err != nil || !bytes.HasPrefix(got.out, []byte("out-1\nout-2\n")) || p.Stderr() != "err-1\nerr-2\n" {
				t.Fatalf("stdout=%q, stderr=%q, Read=%v", got.out, p.Stderr(), got.err)
			}
		case <-ctx.Done():
			t.Fatal("the writing child held the stream after the signal")
		}
		if err := p.Wait(); err == nil || err.Error() != "exit status -1" {
			t.Fatalf("Wait = %v, want the signal's exit status", err)
		}
	})
}

type pipeListener struct {
	accept chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{accept: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accept:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}

func (l *pipeListener) dial() (net.Conn, error) {
	c1, c2 := net.Pipe()
	select {
	case l.accept <- c2:
		return c1, nil
	case <-l.closed:
		_ = c1.Close()
		_ = c2.Close()
		return nil, net.ErrClosed
	}
}

// TLS peers that stop reading block the server's close_notify alert during
// connection close. The shutdown starts its deadline before closing
// connections, fails within its budget, and names the blocked connection close.
func TestFakeEnvdShutdownBoundsBlockedTLSClose(t *testing.T) {
	ln := newPipeListener()
	f := &fakeEnvd{
		t:     t,
		wait:  60 * time.Millisecond,
		done:  make(chan struct{}),
		procs: map[uint32]*fakeCmd{},
		stdin: map[uint32]io.WriteCloser{},
	}
	f.ctx, f.stop = context.WithCancel(context.Background())
	f.srv = httptest.NewUnstartedServer(f)
	_ = f.srv.Listener.Close()
	f.srv.Listener = ln
	f.srv.Config.BaseContext = func(net.Listener) context.Context { return f.ctx }
	f.srv.StartTLS()

	var peers []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		for _, c := range peers {
			_ = c.Close()
		}
		_ = f.shutdown()
		select {
		case <-f.done:
		case <-time.After(30 * time.Second):
			t.Error("the fake envd shutdown goroutine did not finish after peers closed")
		}
	})

	tlsCfg := f.srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tlsCfg.ServerName = "example.com"
	tlsCfg.NextProtos = []string{"http/1.1"}
	for range 2 {
		raw, err := ln.dial()
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, raw)
		tc := tls.Client(raw, tlsCfg)
		if err := tc.Handshake(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tc, "GET /nope HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	err := shutdownWithin(t, f, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "did not shut down in 60ms") || !strings.Contains(err.Error(), "closing the listener and client connections") {
		t.Fatalf("shutdown = %v, want timeout naming connection close", err)
	}
	for _, c := range peers {
		_ = c.Close()
	}
	waitFor(t, "the unblocked shutdown", func() bool {
		select {
		case <-f.done:
			return true
		default:
			return false
		}
	})
}
