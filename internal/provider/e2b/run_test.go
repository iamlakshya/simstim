package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func signals(envd *fakeEnvd) []string {
	envd.mu.Lock()
	defer envd.mu.Unlock()
	return slices.Clone(envd.sigs)
}

// envd keeps a command running when its request drops, so when ctx ends, Run
// returns only after envd has answered its SIGKILL. Its error says why ctx
// ended.
func TestEnvdRunWaitsForTheKillWhenCtxEnds(t *testing.T) {
	e, envd := envdFor(t, false)
	killGate := make(chan struct{})
	envd.mu.Lock()
	envd.killGate = killGate
	envd.mu.Unlock()
	started := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	ran := make(chan error, 1)
	go func() {
		// The input reaches the command only after Start has returned, so
		// the file says that the end of ctx will kill it.
		_, err := e.Run(ctx, fmt.Sprintf("read go; echo yes > %q; exec sleep 30", started), strings.NewReader("go\n"))
		ran <- err
	}()
	waitFor(t, "the command's start", func() bool {
		b, err := os.ReadFile(started)
		return err == nil && string(b) == "yes\n"
	})
	stopped := errors.New("stopped by the test")
	cancel(stopped)
	waitFor(t, "the kill", func() bool { return len(signals(envd)) > 0 })
	select {
	case err := <-ran:
		t.Fatalf("Run returned (%v) before envd answered the kill", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(killGate)
	err := <-ran
	if !errors.Is(err, stopped) {
		t.Fatalf("Run = %v, want the cause of the end of ctx", err)
	}
	if sigs := signals(envd); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// A stream that breaks while ctx lives: envd keeps the command, so Run kills
// it before it returns.
func TestEnvdRunKillsTheCommandWhenTheStreamBreaks(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.drop = true
	envd.mu.Unlock()
	_, err := e.Run(context.Background(), "exec sleep 30", nil)
	if !errors.Is(err, client.ErrUnreachable) {
		t.Fatalf("Run = %v, want ErrUnreachable", err)
	}
	if sigs := signals(envd); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals when Run returned = %v, want one SIGKILL", sigs)
	}
}

// A command with an exit status needs no kill.
func TestEnvdRunSendsNoKillAfterTheExit(t *testing.T) {
	e, envd := envdFor(t, false)
	if _, err := e.Run(context.Background(), "echo ok", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), "exit 3", nil); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("Run = %v, want exit status 3", err)
	}
	if sigs := signals(envd); len(sigs) != 0 {
		t.Fatalf("signals = %v, want none", sigs)
	}
}

// Input that stalls for good: Run says that the input could not be sent, and
// kills the command, which would wait for the rest of it for ever. It does
// not wait for ctx to end.
func TestEnvdRunFailsAndKillsWhenInputStalls(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.gate = make(chan struct{}) // no SendInput call ends
	envd.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := e.Run(ctx, "cat > /dev/null", strings.NewReader(strings.Repeat("x", 3*chunk)))
	requireInputClassification(t, err, "input_send")
	if ctx.Err() != nil {
		t.Fatalf("Run = %v after ctx ended, want the stalled input before", err)
	}
	want := "Error: cannot send the input of a command to the sandbox " + testID + ": stdin was not sent in time: no SendInput call ended for 300ms\n\n" +
		"Try: " + brand.Name + " host status cloud (says whether the sandbox runs)\n"
	if got := rendered(err); got != want {
		t.Fatalf("Run's error, as the CLI shows it:\n%s\nwant:\n%s", got, want)
	}
	if sigs := signals(envd); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// rendered is err as the CLI shows a UserError (printError in internal/cli),
// without colour; "" when err is no UserError.
func rendered(err error) string {
	var ue *client.UserError
	if !errors.As(err, &ue) {
		return ""
	}
	return "Error: " + ue.Msg + "\n\nTry: " + ue.Hint + "\n"
}

// errDisk is a reader that fails after it gave part of the input.
var errDisk = errors.New("the disk failed")

type failingReader struct{ part []byte }

func (r *failingReader) Read(b []byte) (int, error) {
	if len(r.part) == 0 {
		return 0, errDisk
	}
	n := copy(b, r.part)
	r.part = r.part[n:]
	return n, nil
}

// A reader that gives part of the input, then fails: the command gets no EOF,
// so it never takes the part for all of it, and it gets its kill. Run says
// why, even though the command could have ended well.
func TestEnvdRunFailsWhenItsInputReaderFails(t *testing.T) {
	e, envd := envdFor(t, false)
	file := filepath.Join(t.TempDir(), "in")
	out, err := e.Run(context.Background(), fmt.Sprintf("cat > %q; echo done", file), &failingReader{part: []byte("part of the input\n")})
	requireInputClassification(t, err, "input_read")
	if !errors.Is(err, errDisk) || !strings.HasPrefix(rendered(err), "Error: cannot read the input of a command for the sandbox") || !strings.Contains(rendered(err), "\nTry: run the command again\n") {
		t.Fatalf("Run = %q, %v, want the reader's error with a hint", out, err)
	}
	if strings.Contains(string(out), "done") {
		t.Fatalf("the command took a part of its input for all of it: %q", out)
	}
	if sigs := signals(envd); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// lateReader fails once Run has read all the output of a command that ended
// well.
type lateReader struct{ read chan *proc }

func (r *lateReader) Read([]byte) (int, error) {
	p := <-r.read
	for deadline := time.Now().Add(30 * time.Second); !p.exited() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	return 0, errDisk
}

// A command that ends well without its input, which fails after that: Run
// never reports success after its input failed.
func TestEnvdRunNeverSucceedsAfterItsInputFailed(t *testing.T) {
	e, _ := envdFor(t, false)
	r := &lateReader{read: make(chan *proc, 1)}
	old := runRead
	t.Cleanup(func() { runRead = old })
	runRead = func(p *proc) ([]byte, error) {
		out, err := io.ReadAll(p.Stdout())
		r.read <- p
		return out, err
	}
	if _, err := e.Run(context.Background(), "true", r); !errors.Is(err, errDisk) {
		t.Fatalf("Run = %v, want the reader's error", err)
	}
}

// All the input is sent, but its EOF is not: Run says so before ctx ends, with
// a hint, and kills the command, which would wait for the EOF for ever.
func TestEnvdRunFailsWhenItsEOFFails(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.failEOF = true
	envd.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := e.Run(ctx, "cat > /dev/null", strings.NewReader("all of the input"))
	if ctx.Err() != nil {
		t.Fatalf("Run = %v after ctx ended, want the failed EOF before", err)
	}
	if got := rendered(err); !strings.Contains(got, "send the end of the input: envd CloseStdin: HTTP 503: no healthy upstream") || !strings.Contains(got, "\nTry: "+brand.Name+" host status cloud") {
		t.Fatalf("Run's error, as the CLI shows it:\n%s\n(%v)", got, err)
	}
	if sigs := signals(envd); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// A command that ends on its own while its input is still being sent, as the
// install does when mkdir fails: Run reports the command's exit status, not
// an input error. envd answers the writes that follow with internal while it
// still knows the command, and with not_found after it passed on the end.
func TestEnvdRunReportsTheCommandsOwnEnd(t *testing.T) {
	for _, gone := range []bool{false, true} {
		for _, tc := range []struct {
			name, script string
			input, want  string
		}{
			{"install", "set -e; mkdir -p /dev/null/x; cat > /dev/null; echo __INSTALLED__", strings.Repeat("x", 9<<20), "exit status 1: mkdir"},
			{"exit", "exit 3", strings.Repeat("x", 4<<20), "exit status 3"},
		} {
			t.Run(fmt.Sprintf("%s/not_found=%v", tc.name, gone), func(t *testing.T) {
				e, envd := envdFor(t, false)
				envd.mu.Lock()
				envd.writeGone = gone
				envd.mu.Unlock()
				for range 5 {
					out, err := e.Run(context.Background(), tc.script, strings.NewReader(tc.input))
					if err == nil || !strings.Contains(err.Error(), tc.want) || rendered(err) != "" {
						t.Fatalf("Run = %q, %v, want %q and no input error", out, err, tc.want)
					}
				}
			})
		}
	}
}

// releasePipeOnSignal lets the fake's output readers run only after the
// signal has finished its output cut. The old close loses every queued byte.
type releasePipeOnSignal struct {
	gate chan struct{}
	once sync.Once
}

func (r *releasePipeOnSignal) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if strings.HasSuffix(req.URL.Path, "/SendSignal") {
		r.once.Do(func() { close(r.gate) })
	}
	return resp, err
}

func TestEnvdRunKeepsBufferedOutputAfterTheCommandEnds(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, gone := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/not_found=%v", stream, gone), func(t *testing.T) {
				e, f := envdFor(t, stream)
				gate := make(chan struct{})
				e.HTTP = &http.Client{Transport: &releasePipeOnSignal{gate: gate}}
				f.mu.Lock()
				f.pipeGate, f.writeGone = gate, gone
				f.mu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				script := "sleep 120 & printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2; exit 1"
				out, err := e.Run(ctx, script, strings.NewReader(strings.Repeat("x", 9<<20)))
				if string(out) != "out-1\nout-2\n" || err == nil || err.Error() != "exit status 1: err-1; err-2" || rendered(err) != "" {
					t.Fatalf("Run=%q,%v, rendered=%q, want ordered output and the natural exit's stderr", out, err, rendered(err))
				}
			})
		}
	}
}

func TestEnvdRunKeepsBufferedOutputAfterAnInputFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			e, f := envdFor(t, stream)
			gate, inputGate := make(chan struct{}), make(chan struct{})
			e.HTTP = &http.Client{Transport: &releasePipeOnSignal{gate: gate}}
			f.mu.Lock()
			f.pipeGate, f.gate, f.failEOF = gate, inputGate, true
			f.mu.Unlock()
			ready := filepath.Join(t.TempDir(), "ready")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			type result struct {
				out []byte
				err error
			}
			done := make(chan result, 1)
			go func() {
				out, err := e.Run(ctx, fmt.Sprintf("printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2; echo ready > %q; cat > /dev/null", ready), strings.NewReader("the input"))
				done <- result{out, err}
			}()
			waitFor(t, "output before the input failure", func() bool { _, err := os.Stat(ready); return err == nil })
			close(inputGate)
			var got result
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal("Run did not end after the failed input")
			}
			msg := rendered(got.err)
			if string(got.out) != "out-1\nout-2\n" || !strings.Contains(msg, "CloseStdin: HTTP 503") || !strings.Contains(msg, "err-1; err-2") || !strings.Contains(msg, "\nTry: "+brand.Name+" host status cloud") {
				t.Fatalf("Run=%q,%v, rendered=%q, want buffered output, the input error and its hint", got.out, got.err, msg)
			}
		})
	}
}

// The input stalls, and its SIGKILL waits for envd's answer, when ctx ends:
// the end of ctx asks for no second SIGKILL, and Run waits for the answer to
// the first. Every path that kills shares one request.
func TestEnvdRunSendsOneKill(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	e, envd := envdFor(t, false)
	killGate := make(chan struct{})
	envd.mu.Lock()
	envd.gate = make(chan struct{}) // no SendInput call ends
	envd.killGate = killGate
	envd.mu.Unlock()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	ran := make(chan error, 1)
	go func() {
		_, err := e.Run(ctx, "cat > /dev/null", strings.NewReader(strings.Repeat("x", 3*chunk)))
		ran <- err
	}()
	waitFor(t, "the input's SIGKILL", func() bool { return len(signals(envd)) > 0 })
	stopped := errors.New("stopped by the test")
	cancel(stopped)
	select {
	case err := <-ran:
		t.Fatalf("Run returned (%v) before envd answered the SIGKILL", err)
	case <-time.After(200 * time.Millisecond):
	}
	if sigs := signals(envd); len(sigs) != 1 {
		t.Fatalf("signals while the first waits = %v, want one", sigs)
	}
	close(killGate)
	if err := <-ran; !errors.Is(err, stopped) {
		t.Fatalf("Run = %v, want the cause of the end of ctx", err)
	}
	if sigs := signals(envd); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// The input stalls, and the command's end event comes only after endWait:
// send's SIGKILL is the only one. The kill that follows asks for none.
func TestEnvdRunSendsOneKillWhenTheEndIsLate(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	old := endWait
	endWait = 200 * time.Millisecond
	t.Cleanup(func() { endWait = old })
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.gate = make(chan struct{})    // no SendInput call ends
	envd.endGate = make(chan struct{}) // the end event waits
	envd.mu.Unlock()
	_, err := e.Run(context.Background(), "cat > /dev/null", strings.NewReader(strings.Repeat("x", 3*chunk)))
	if !strings.Contains(rendered(err), "no SendInput call ended") {
		t.Fatalf("Run = %v, want the stalled input", err)
	}
	if sigs := signals(envd); len(sigs) != 1 {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// In stream mode, a StreamInput request that ends with an error part way:
// Run says that the input failed, and kills the command, which got no EOF and
// so never took the part for all of its input.
func TestEnvdRunFailsWhenTheInputStreamIsCut(t *testing.T) {
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.cutStream = 32 << 10
	envd.mu.Unlock()
	file := filepath.Join(t.TempDir(), "in")
	out, err := e.Run(context.Background(), fmt.Sprintf("cat > %q; echo done", file), strings.NewReader(strings.Repeat("x", 1<<20)))
	if !strings.Contains(rendered(err), "the link broke") || strings.Contains(string(out), "done") {
		t.Fatalf("Run = %q, %v, want the cut input and no EOF", out, err)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent after a cut input")
	}
	if sigs := signals(envd); len(sigs) != 1 {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// In stream mode, a StreamInput request that envd does not read: Run gives up
// after the stall limit, sends no EOF before the input, and kills the
// command. It never reports success.
func TestEnvdRunFailsWhenTheInputStreamIsNotRead(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.gate = make(chan struct{}) // the request is not read
	envd.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // a stall that regresses fails, and does not hang
	defer cancel()
	_, err := e.Run(ctx, "cat > /dev/null", strings.NewReader(strings.Repeat("x", 16<<20)))
	if !strings.Contains(rendered(err), "took no input for 300ms") {
		t.Fatalf("Run = %v, want the stalled stream", err)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent before the input")
	}
	if sigs := signals(envd); len(sigs) != 1 {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// In stream mode, envd reads the request slowly and steadily, through a
// buffer of 1 MiB that is still full when the transport has read the pipe's
// EOF. During the stream the transport's reads of the pipe are the progress,
// and after EOF Close waits the stall limit plus streamTransitWait for the
// buffered input to reach envd and for envd to answer. Close starts with at
// least 400 ms of buffered input left, twice the 200 ms stall limit, so a
// Close without the transit bound would fail early.
func TestEnvdRunFollowsASlowSteadyStream(t *testing.T) {
	shortStall(t, 200*time.Millisecond)
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.streamRate = 2560 << 10 // 64 KiB every 25 ms
	envd.mu.Unlock()
	in := strings.Repeat("x", 5<<18) // 1.7 MiB in base64: more than the buffer
	out, err := e.Run(context.Background(), "wc -c", strings.NewReader(in))
	if err != nil || strings.TrimSpace(string(out)) != fmt.Sprint(len(in)) {
		t.Fatalf("Run = %q, %v, want all %d bytes of the input", out, err, len(in))
	}
}

// When a buffer on the path holds more than the transit bound covers, Close
// gives up early, safely: it sends no CloseStdin, kills the command, and Run
// reports no success.
func TestEnvdRunGivesUpSafelyWhenABufferExceedsTransit(t *testing.T) {
	shortStall(t, 120*time.Millisecond)
	shortTransit(t, 60*time.Millisecond)
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.streamRate = 2 << 20 // 1 MiB buffered takes 500 ms, past the 180 ms bound
	envd.mu.Unlock()
	file := filepath.Join(t.TempDir(), "in")
	in := strings.Repeat("x", 5<<18)
	out, err := e.Run(context.Background(), fmt.Sprintf("cat > %q; echo done", file), strings.NewReader(in))
	if !strings.Contains(rendered(err), "did not answer the StreamInput request for 180ms after its last byte") || strings.Contains(string(out), "done") {
		t.Fatalf("Run = %q, %v, want early stall and no success", out, err)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent after Close gave up early")
	}
	if sigs := signals(envd); len(sigs) != 1 {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// In stream mode, envd stops reading part way and keeps the connection: Run
// fails at the stall limit, sends no EOF, and kills the command.
func TestEnvdRunFailsWhenTheInputStreamStops(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.stopStream = 1 << 20
	envd.mu.Unlock()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // a stall that regresses fails, and does not hang
	defer cancel()
	_, err := e.Run(ctx, "cat > /dev/null", strings.NewReader(strings.Repeat("x", 16<<20)))
	if !strings.Contains(rendered(err), "took no input for 300ms") {
		t.Fatalf("Run = %v, want the stall", err)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("Run failed after %v, with a stall limit of 300ms", took)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent after a stalled input")
	}
	if sigs := signals(envd); len(sigs) != 1 {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// In stream mode, a StreamInput request that envd reads late, but before the
// stall limit: Run sends the EOF only after the whole input, so the command
// takes all of it.
func TestEnvdRunWaitsForAStreamReadLate(t *testing.T) {
	e, envd := envdFor(t, true)
	gate := make(chan struct{})
	envd.mu.Lock()
	envd.gate = gate // the request is read late
	envd.mu.Unlock()
	late := time.AfterFunc(300*time.Millisecond, func() { close(gate) })
	defer late.Stop()
	in := strings.Repeat("x", 3*chunk)
	out, err := e.Run(context.Background(), "wc -c", strings.NewReader(in))
	if err != nil || strings.TrimSpace(string(out)) != fmt.Sprint(len(in)) {
		t.Fatalf("Run = %q, %v, want all %d bytes of the input", out, err, len(in))
	}
}

// eofWrapper gives part of the input, then an error that wraps io.EOF. Only
// io.EOF itself ends a reader's input, as for io.Copy.
type eofWrapper struct{ part []byte }

func (r *eofWrapper) Read(b []byte) (int, error) {
	if len(r.part) == 0 {
		return 0, fmt.Errorf("read the upload: %w", io.EOF)
	}
	n := copy(b, r.part)
	r.part = r.part[n:]
	return n, nil
}

// A reader whose error wraps io.EOF has failed: Run says so, and sends no
// EOF.
func TestEnvdRunFailsOnAReaderErrorThatWrapsEOF(t *testing.T) {
	e, envd := envdFor(t, false)
	file := filepath.Join(t.TempDir(), "in")
	out, err := e.Run(context.Background(), fmt.Sprintf("cat > %q; echo done", file), &eofWrapper{part: []byte("part of the input\n")})
	if !strings.HasPrefix(rendered(err), "Error: cannot read the input of a command") || strings.Contains(string(out), "done") {
		t.Fatalf("Run = %q, %v, want the reader's failure", out, err)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent after the reader failed")
	}
}

// Run's error ends with the command's stderr, also when ctx ended and when
// the input failed (the Transport's contract).
func TestEnvdRunKeepsTheStderrTail(t *testing.T) {
	old := runRead
	t.Cleanup(func() { runRead = old })
	hasReason := func(p *proc) bool { return strings.Contains(p.Stderr(), "the-reason") }
	t.Run("ctx ended", func(t *testing.T) {
		e, _ := envdFor(t, false)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runRead = func(p *proc) ([]byte, error) {
			waitFor(t, "the command's stderr", func() bool { return hasReason(p) })
			cancel()
			return io.ReadAll(p.Stdout())
		}
		_, err := e.Run(ctx, "echo the-reason >&2; exec sleep 30", nil)
		if !errors.Is(err, context.Canceled) || !strings.HasSuffix(err.Error(), ": the-reason") {
			t.Fatalf("Run = %v, want the end of ctx and the stderr", err)
		}
	})
	t.Run("input failed", func(t *testing.T) {
		shortStall(t, 300*time.Millisecond)
		e, envd := envdFor(t, false)
		envd.mu.Lock()
		envd.gate = make(chan struct{}) // no SendInput call ends
		envd.mu.Unlock()
		runRead = func(p *proc) ([]byte, error) {
			waitFor(t, "the command's stderr", func() bool { return hasReason(p) })
			return io.ReadAll(p.Stdout())
		}
		_, err := e.Run(context.Background(), "echo the-reason >&2; cat > /dev/null", strings.NewReader(strings.Repeat("x", 3*chunk)))
		if got := rendered(err); !strings.Contains(got, "no SendInput call ended for 300ms: the-reason\n") || !strings.Contains(got, "\nTry: ") {
			t.Fatalf("Run's error, as the CLI shows it:\n%s", got)
		}
	})
}

// The reader fails, and ctx ends while envd holds the input's SIGKILL. The
// error that the CLI shows still ends with the command's stderr, once, and
// the error wraps both the cause of the end of ctx and the reader's error.
func TestEnvdRunKeepsTheStderrTailWhenCtxEndsAfterAnInputFailure(t *testing.T) {
	e, envd := envdFor(t, false)
	killGate := make(chan struct{})
	envd.mu.Lock()
	envd.killGate = killGate
	envd.mu.Unlock()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stopped := errors.New("stopped by the test")
	old := runRead
	t.Cleanup(func() { runRead = old })
	runRead = func(p *proc) ([]byte, error) {
		waitFor(t, "the command's stderr", func() bool { return strings.Contains(p.Stderr(), "the-reason") })
		// The input's SIGKILL comes after the input's error is kept.
		waitFor(t, "the input's SIGKILL", func() bool { return len(signals(envd)) > 0 })
		cancel(stopped)
		close(killGate)
		return io.ReadAll(p.Stdout())
	}
	_, err := e.Run(ctx, "echo the-reason >&2; cat > /dev/null", &failingReader{part: []byte("part of the input\n")})
	got := rendered(err)
	if !strings.HasPrefix(got, "Error: cannot read the input of a command") || !strings.Contains(got, ": the-reason\n\nTry: ") {
		t.Fatalf("Run's error, as the CLI shows it:\n%s", got)
	}
	if !errors.Is(err, stopped) || !errors.Is(err, errDisk) {
		t.Fatalf("Run = %v, want the cause of the end of ctx and the reader's error", err)
	}
	if n := strings.Count(err.Error(), "the-reason"); n != 1 {
		t.Fatalf("Run = %v, with the stderr %d times, want once", err, n)
	}
}

// A command that fails says so with its stderr at the end, and with nothing
// after its status when its stderr is empty.
func TestEnvdRunEndsItsErrorWithTheStderrOnly(t *testing.T) {
	e, _ := envdFor(t, false)
	for script, end := range map[string]string{
		"exit 3":                      "exit status 3",
		"echo the-reason >&2; exit 3": "exit status 3: the-reason",
	} {
		_, err := e.Run(context.Background(), script, nil)
		if err == nil || !strings.HasSuffix(err.Error(), end) {
			t.Errorf("Run(%q) = %v, want it to end with %q", script, err, end)
		}
	}
}

// A stream that breaks while the input is still being sent: Run reports the
// broken stream. Its own kill then cuts the input, which is no input error.
func TestEnvdRunReportsABrokenStreamDuringInput(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.drop = true
	envd.gate = make(chan struct{}) // no SendInput call ends
	envd.mu.Unlock()
	_, err := e.Run(context.Background(), "cat > /dev/null", strings.NewReader(strings.Repeat("x", 3<<20)))
	if !errors.Is(err, client.ErrUnreachable) || rendered(err) != "" {
		t.Fatalf("Run = %v, want the broken stream", err)
	}
	if sigs := signals(envd); len(sigs) != 1 {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// blockedReader blocks until the test ends, and says when it is first read.
type blockedReader struct {
	reading chan struct{}
	once    sync.Once
	done    chan struct{}
}

func (r *blockedReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.reading) })
	<-r.done
	return 0, io.EOF
}

// A reader that blocks for ever, then ctx ends: Run returns once the kill is
// answered and inputWait has passed. The copy's goroutine ends with its
// reader.
func TestEnvdRunDoesNotWaitForABlockedReader(t *testing.T) {
	old := inputWait
	inputWait = 300 * time.Millisecond
	t.Cleanup(func() { inputWait = old })
	e, envd := envdFor(t, false)
	r := &blockedReader{reading: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(func() { close(r.done) })
	ctx, cancel := context.WithCancelCause(context.Background())
	stopped := errors.New("stopped by the test")
	ran := make(chan error, 1)
	go func() {
		_, err := e.Run(ctx, "cat > /dev/null", r)
		ran <- err
	}()
	<-r.reading // Run has started the command, and copies its input
	start := time.Now()
	cancel(stopped)
	select {
	case err := <-ran:
		if !errors.Is(err, stopped) {
			t.Fatalf("Run = %v, want the cause of the end of ctx", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run waited for a reader that blocks")
	}
	t.Logf("Run returned %v after ctx ended", time.Since(start).Round(time.Millisecond))
	if sigs := signals(envd); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals = %v, want one SIGKILL", sigs)
	}
}

// ctx ends after the command ended well, and its kill closes the output pipe
// before Run reads it: Run's error says that ctx ended, not only that the pipe
// was closed.
func TestEnvdRunSaysCtxEndedWhenItClosedTheOutput(t *testing.T) {
	e, _ := envdFor(t, false)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stopped := errors.New("stopped by the test")
	old := runRead
	t.Cleanup(func() { runRead = old })
	runRead = func(p *proc) ([]byte, error) {
		<-p.done // the command and its stream ended well
		cancel(stopped)
		waitFor(t, "the kill", p.killing.Load)
		p.Kill() // waits for the kill, which closed the pipe
		return io.ReadAll(p.Stdout())
	}
	_, err := e.Run(ctx, "true", nil)
	if !errors.Is(err, stopped) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Run = %v, want the cause of the end of ctx, and the closed pipe", err)
	}
}

// raceEnvd answers Start with a command that prints a line and ends when the
// test says, so that its end can race the end of ctx.
type raceEnvd struct {
	mu      sync.Mutex
	sigs    int
	ready   chan struct{}
	proceed chan struct{}
}

func newRaceEnvd(t *testing.T) (*raceEnvd, *Envd) {
	r := &raceEnvd{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/Start"):
			_, _, _ = readEnvelope(req.Body)
			r.mu.Lock()
			ready, proceed := r.ready, r.proceed
			r.mu.Unlock()
			w.Header().Set("Content-Type", contentStream)
			w.WriteHeader(http.StatusOK)
			fw := &flushWriter{w: w}
			fw.send(0, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 42}}})
			ready <- struct{}{}
			select {
			case <-proceed:
			case <-req.Context().Done():
				return
			}
			fw.send(0, map[string]any{"event": map[string]any{"data": map[string]any{"stdout": []byte("hi\n")}}})
			fw.send(0, map[string]any{"event": map[string]any{"end": map[string]any{"exited": true}}})
			fw.send(flagEndStream, map[string]any{})
		case strings.HasSuffix(req.URL.Path, "/SendSignal"):
			r.mu.Lock()
			r.sigs++
			r.mu.Unlock()
			_, _ = w.Write([]byte("{}"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	return r, &Envd{URL: srv.URL, HTTP: srv.Client(), SandboxID: "x"}
}

// ctx ends as the command ends, in every order: Run never kills twice, never
// after it returned, and a success has the whole output. A failure says that
// ctx ended, even when what broke was the read of the output.
func TestEnvdRunWhenCtxEndsAsTheCommandEnds(t *testing.T) {
	r, e := newRaceEnvd(t)
	rng := rand.New(rand.NewPCG(5, 6))
	for i := range 50 {
		r.mu.Lock()
		r.sigs = 0
		r.ready = make(chan struct{}, 1)
		r.proceed = make(chan struct{})
		ready, proceed := r.ready, r.proceed
		r.mu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		type result struct {
			out []byte
			err error
		}
		ran := make(chan result, 1)
		go func() { out, err := e.Run(ctx, "x", nil); ran <- result{out, err} }()
		<-ready
		d1, d2 := time.Duration(rng.IntN(300))*time.Microsecond, time.Duration(rng.IntN(300))*time.Microsecond
		go func() { time.Sleep(d1); cancel() }()
		go func() { time.Sleep(d2); close(proceed) }()
		var got result
		select {
		case got = <-ran:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: Run did not return", i)
		}
		r.mu.Lock()
		atReturn := r.sigs
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		r.mu.Lock()
		later := r.sigs
		r.mu.Unlock()
		switch {
		case later > 1:
			t.Fatalf("round %d: %d kills", i, later)
		case later != atReturn:
			t.Fatalf("round %d: a kill after Run returned", i)
		case got.err == nil && string(got.out) != "hi\n":
			t.Fatalf("round %d: success with %q", i, got.out)
		case got.err != nil && !errors.Is(got.err, context.Canceled):
			t.Fatalf("round %d: Run = %v, want it to say that ctx ended", i, got.err)
		}
		cancel()
	}
}

// Cancellation or failure after Start's body has gone out, before StartEvent
// gives a process ID, kills the command synchronously by its Start tag before
// Start returns. An already-cancelled context sends neither Start nor
// SendSignal.
func TestRunCancelsBeforeStartEventKillsByTag(t *testing.T) {
	t.Run("already cancelled ctx", func(t *testing.T) {
		e, envd := envdFor(t, false)
		ctx, cancel := context.WithCancelCause(context.Background())
		stopped := errors.New("stopped before start")
		cancel(stopped)
		_, err := e.Run(ctx, "echo nope", nil)
		if !errors.Is(err, stopped) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want stopped and Canceled", err)
		}
		if paths := envd.Paths(); len(paths) != 0 {
			t.Fatalf("paths = %v, want no Start or SendSignal", paths)
		}
	})

	for _, tc := range []struct {
		name        string
		afterHeader bool
	}{
		{"before response headers", false},
		{"before StartEvent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, envd := envdFor(t, false)
			envd.mu.Lock()
			if tc.afterHeader {
				envd.startGate = make(chan struct{})
			} else {
				envd.startHeaderGate = make(chan struct{})
			}
			envd.mu.Unlock()

			started := filepath.Join(t.TempDir(), "started")
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			ran := make(chan error, 1)
			go func() {
				_, err := e.Run(ctx, fmt.Sprintf("echo yes > %q; exec sleep 30", started), nil)
				ran <- err
			}()

			waitFor(t, "the command's start before StartEvent", func() bool {
				if tc.afterHeader && !envd.inStartGate.Load() {
					return false
				}
				b, err := os.ReadFile(started)
				return err == nil && string(b) == "yes\n"
			})

			stopped := errors.New("stopped before StartEvent")
			cancel(stopped)
			var err error
			select {
			case err = <-ran:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return in 5s after ctx ended")
			}
			if !errors.Is(err, stopped) {
				t.Fatalf("Run = %v, want the cause of the end of ctx", err)
			}

			envd.mu.Lock()
			tags := slices.Clone(envd.tags)
			reqs := slices.Clone(envd.sigReqs)
			var c *fakeCmd
			if len(tags) == 1 {
				c = envd.byTag[tags[0]]
			}
			envd.mu.Unlock()

			if len(tags) != 1 || !strings.HasPrefix(tags[0], brand.Name+"-") {
				t.Fatalf("tags = %v, want one tagged Start", tags)
			}
			want := signalRequest{Process: selector{Tag: tags[0]}, Signal: "SIGNAL_SIGKILL"}
			if len(reqs) != 1 || reqs[0] != want {
				t.Fatalf("signal requests = %+v, want [%+v]", reqs, want)
			}
			if c == nil {
				t.Fatal("fake envd did not record the command by tag")
			}
			select {
			case <-c.ended:
			default:
				t.Fatal("the spawned command was still running when Run returned")
			}
			if c.status != "signal killed" {
				t.Fatalf("command status = %q, want signal killed", c.status)
			}
		})
	}

	t.Run("signal arrives before Start launches", func(t *testing.T) {
		e, envd := envdFor(t, false)
		tag := brand.Name + "-pretag"
		if err := e.unary(context.Background(), "SendSignal", signalRequest{
			Process: selector{Tag: tag},
			Signal:  "SIGNAL_SIGKILL",
		}); err != nil {
			t.Fatal(err)
		}
		ran := filepath.Join(t.TempDir(), "ran")
		msg, err := json.Marshal(startRequest{
			Process: processConfig{Cmd: "/bin/sh", Args: []string{"-c", fmt.Sprintf("echo ran > %q", ran)}},
			Tag:     tag,
			Stdin:   true,
		})
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		if err := writeEnvelope(&body, 0, msg); err != nil {
			t.Fatal(err)
		}
		req, err := e.request(context.Background(), "Start", &body, true)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := e.client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if _, statErr := os.Stat(ran); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("stat %s = %v, want pre-signalled tag not to run", ran, statErr)
		}
		envd.mu.Lock()
		c := envd.byTag[tag]
		envd.mu.Unlock()
		if c != nil {
			t.Fatal("pre-signalled tag spawned a wrapper")
		}
	})
}
