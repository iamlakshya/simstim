package e2b

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/ring"
)

func stderrTestEnvd(t *testing.T, events [][]byte) *Envd {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/Start") {
			w.Header().Set("Content-Type", contentUnary)
			_, _ = w.Write([]byte("{}"))
			return
		}
		w.Header().Set("Content-Type", contentStream)
		fw := &flushWriter{w: w}
		fw.send(0, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 7}}})
		for _, b := range events {
			fw.send(0, map[string]any{"event": map[string]any{"data": map[string]any{"stderr": b}}})
		}
		fw.send(0, map[string]any{"event": map[string]any{"end": map[string]any{"exitCode": 1, "exited": true}}})
		fw.send(flagEndStream, map[string]any{})
	}))
	t.Cleanup(srv.Close)
	return &Envd{URL: srv.URL, HTTP: srv.Client(), SandboxID: "fake"}
}

func stderrEvents(b []byte, size int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n := min(len(b), size)
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}

func TestEnvdRunStderrTailIsIndependentOfEventBoundaries(t *testing.T) {
	stderr := []byte("HEAD_PREFIX\n" + strings.Repeat("early filler line\n", 768) + "last-1\nlast-2\nFINAL_FATAL_REASON: disk full\n")
	for _, size := range []int{len(stderr), 4096, 1} {
		t.Run(fmt.Sprintf("event_bytes=%d", size), func(t *testing.T) {
			e := stderrTestEnvd(t, stderrEvents(stderr, size))
			_, err := e.Run(context.Background(), "unused", nil)
			if err == nil || err.Error() != "exit status 1: last-1; last-2; FINAL_FATAL_REASON: disk full" {
				t.Fatalf("Run = %v, want the final three stderr lines", err)
			}
		})
	}
}

func TestEnvdStartStillKeepsStderrHeadWithoutTailBuffer(t *testing.T) {
	stderr := []byte("HEAD_PREFIX\n" + strings.Repeat("early filler line\n", 768) + "FINAL_FATAL_REASON\n")
	e := stderrTestEnvd(t, stderrEvents(stderr, 4096))
	p, err := e.Start(context.Background(), "unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Kill)
	_, _ = io.Copy(io.Discard, p.Stdout())
	if err := p.Wait(); err == nil {
		t.Fatal("expected command failure")
	}
	if !strings.HasPrefix(p.Stderr(), "HEAD_PREFIX\n") || strings.Contains(p.Stderr(), "FINAL_FATAL_REASON") {
		t.Fatalf("Stderr changed its head contract: %q", p.Stderr())
	}
	if p.(*proc).runStderr != nil {
		t.Fatal("ordinary Start allocated Run's diagnostic ring")
	}
}

func TestEnvdRunStderrTailDoesNotStartInsideUTF8Rune(t *testing.T) {
	stderr := []byte(strings.Repeat("€", 3000) + "\nlast\nfatal\n")
	e := stderrTestEnvd(t, stderrEvents(stderr, 4096))
	_, err := e.Run(context.Background(), "unused", nil)
	if err == nil || !utf8.ValidString(err.Error()) || !strings.HasSuffix(err.Error(), "; last; fatal") {
		t.Fatalf("Run error must keep a valid UTF8 tail and its final lines: %v", err)
	}
}

func TestEnvdRunCaptureOptionsKeepOnly8KiB(t *testing.T) {
	stderr := bytes.Repeat([]byte("x"), 24<<10)
	e := stderrTestEnvd(t, stderrEvents(stderr, 4096))
	c, err := e.start(context.Background(), "unused", startOptions{stderrTail: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Kill)
	_, _ = io.Copy(io.Discard, c.Stdout())
	_ = c.Wait()
	p := c.(*proc)
	p.mu.Lock()
	retained := p.runStderr.End() - p.runStderr.Start()
	p.mu.Unlock()
	if retained != 8<<10 || len(p.stderrTail()) != 8<<10 {
		t.Fatalf("retained=%d, tail=%d, want the 8 KiB Run limit", retained, len(p.stderrTail()))
	}
}

func TestRunStderrTailKeepsItsBoundAndUTF8Boundary(t *testing.T) {
	p := &proc{runStderr: ring.New(stderrTailBytes)}
	want := []byte(strings.Repeat("€", 3000) + "\nlast\nfatal\n")
	for _, b := range want {
		p.captureStderr([]byte{b})
	}
	p.mu.Lock()
	retained := p.runStderr.End() - p.runStderr.Start()
	p.mu.Unlock()
	got := p.stderrTail()
	if retained != 8<<10 || len(got) > 8<<10 || !utf8.ValidString(got) || !strings.HasSuffix(got, "\nlast\nfatal\n") {
		t.Fatalf("retained=%d, bytes=%d, valid=%v, want a bounded valid tail", retained, len(got), utf8.ValidString(got))
	}
}

func TestRunStderrTailConcurrentCaptureAndRead(t *testing.T) {
	p := &proc{runStderr: ring.New(stderrTailBytes)}
	done := make(chan struct{})
	want := bytes.Repeat([]byte("a"), 3*stderrTailBytes)
	go func() {
		defer close(done)
		for _, b := range want {
			p.captureStderr([]byte{b})
		}
	}()
	for {
		select {
		case <-done:
			if got := p.stderrTail(); got != strings.Repeat("a", stderrTailBytes) {
				t.Fatalf("tail length = %d, want %d identical bytes", len(got), stderrTailBytes)
			}
			return
		default:
			if got := p.stderrTail(); len(got) > stderrTailBytes {
				t.Fatalf("tail grew to %d bytes", len(got))
			}
			_ = p.Stderr()
		}
	}
}

func BenchmarkRunStderrCapture(b *testing.B) {
	for _, size := range []int{1, 4096} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			p := &proc{runStderr: ring.New(stderrTailBytes)}
			p.captureStderr(bytes.Repeat([]byte("x"), 8192))
			data := bytes.Repeat([]byte("x"), size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p.captureStderr(data)
			}
		})
	}
}
