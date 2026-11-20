package e2b

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// One large write, as the install's upload makes, queues at most maxPending
// and waits to queue the rest. Before, it queued all of it at once, and Close
// had 10 s to send everything. A slow link or a busy machine then lost the
// end of the upload, and the command waited for its input until ctx ended.
func TestSendWriterQueuesAtMostMaxPending(t *testing.T) {
	e, envd := envdFor(t, false)
	gate := make(chan struct{})
	envd.mu.Lock()
	envd.gate = gate
	envd.mu.Unlock()
	file := filepath.Join(t.TempDir(), "in")
	p, err := e.Start(context.Background(), fmt.Sprintf("cat > %q", file))
	if err != nil {
		t.Fatal(err)
	}
	w := p.Stdin().(*sendWriter)
	queued := func() int {
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.buf)
	}
	// An odd first write goes alone and waits at the gate, so every later
	// part starts at an odd offset, and the last part is odd. A full queue
	// waits behind it, and the rest must wait in Write.
	// TestSendWriterKeepsEveryByteOfOddParts has an odd total.
	in := make([]byte, 13+maxPending+2*chunk+101)
	for i := range in {
		in[i] = byte(i % 251)
	}
	go func() { _, _ = io.Copy(io.Discard, p.Stdout()) }()
	if _, err := w.Write(in[:13]); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first SendInput call", func() bool { return strings.Contains(strings.Join(envd.Paths(), " "), "SendInput") })
	wrote := make(chan error, 1)
	go func() {
		_, err := w.Write(in[13:])
		wrote <- err
	}()
	waitFor(t, "a full queue", func() bool { return queued() >= maxPending })
	select {
	case err := <-wrote:
		t.Fatalf("Write returned (%v) while the first call was held, with %d bytes queued", err, queued())
	case <-time.After(100 * time.Millisecond):
	}
	if n := queued(); n > maxPending {
		t.Fatalf("%d bytes queued, want at most %d", n, maxPending)
	}
	close(gate)
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The bytes arrive whole and in order, as the attach path needs.
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if out, err := os.ReadFile(file); err != nil || !bytes.Equal(out, in) {
		t.Fatalf("cat wrote %d of %d bytes (equal: %v), %v", len(out), len(in), bytes.Equal(out, in), err)
	}
}

// A Write waiting for room in the queue returns the bytes already queued when
// stdin is closed or the command's context ends, and the send loop exits.
func TestSendWriterUnblocksWriteOnCloseAndCancel(t *testing.T) {
	e, envd := envdFor(t, false)
	gate := make(chan struct{})
	envd.mu.Lock()
	envd.gate = gate
	envd.mu.Unlock()

	p, err := e.Start(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, p.Stdout()) }()
	w := p.Stdin().(*sendWriter)
	queued := func(w *sendWriter) int {
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.buf)
	}
	type writeResult struct {
		n   int
		err error
	}
	wrote := make(chan writeResult, 1)
	go func() {
		n, err := w.Write(make([]byte, maxPending+2*chunk))
		wrote <- writeResult{n, err}
	}()
	waitFor(t, "a full queue", func() bool { return queued(w) >= maxPending })
	closed := make(chan error, 1)
	go func() { closed <- w.Close() }()
	res := <-wrote
	if res.n < maxPending || !errors.Is(res.err, io.ErrClosedPipe) {
		t.Fatalf("Write on Close = (%d, %v), want at least %d and ErrClosedPipe", res.n, res.err, maxPending)
	}
	close(gate)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}

	gate2 := make(chan struct{})
	envd.mu.Lock()
	envd.gate = gate2
	envd.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	p2, err := e.Start(ctx, "cat")
	if err != nil {
		t.Fatal(err)
	}
	w2 := p2.Stdin().(*sendWriter)
	wrote2 := make(chan writeResult, 1)
	go func() {
		n, err := w2.Write(make([]byte, maxPending+2*chunk))
		wrote2 <- writeResult{n, err}
	}()
	waitFor(t, "a full queue", func() bool { return queued(w2) >= maxPending })
	cancel()
	res2 := <-wrote2
	if res2.n < maxPending || !errors.Is(res2.err, context.Canceled) {
		t.Fatalf("Write on cancel = (%d, %v), want at least %d and context.Canceled", res2.n, res2.err, maxPending)
	}
	select {
	case <-w2.done:
	case <-time.After(5 * time.Second):
		t.Fatal("sendWriter loop did not exit when ctx ended")
	}
}

// shortStall sets the stall limit of the writers that the test starts.
func shortStall(t *testing.T, d time.Duration) {
	t.Helper()
	old := stallLimit
	stallLimit = d
	t.Cleanup(func() { stallLimit = old })
}

// slowLink lets one held SendInput call end every step, until the test ends.
func slowLink(t *testing.T, gate chan struct{}, step time.Duration) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		tick := time.NewTicker(step)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
			case <-done:
				return
			}
			select {
			case gate <- struct{}{}:
			case <-done:
				return
			}
		}
	}()
}

// A slow link that still moves the input: Close sends every byte, then EOF,
// though the last part takes longer than the stall limit. This is a slow
// upload at a tenth of the time: one call ends every 100 ms, and the limit
// is 1 s. Before, Close gave up on a fixed budget and dropped the rest.
func TestSendWriterCloseWaitsWhileInputMoves(t *testing.T) {
	shortStall(t, time.Second)
	e, envd := envdFor(t, false)
	gate := make(chan struct{})
	envd.mu.Lock()
	envd.gate = gate
	envd.mu.Unlock()
	slowLink(t, gate, 100*time.Millisecond)
	p, err := e.Start(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	in := make([]byte, maxPending+chunk+7) // 18 calls, 17 of them after Write returns
	for i := range in {
		in[i] = byte(i % 253)
	}
	got := make(chan []byte, 1)
	go func() {
		out, _ := io.ReadAll(p.Stdout())
		got <- out
	}()
	if _, err := p.Stdin().Write(in); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Stdin().Close(); err != nil {
		t.Fatalf("Close after %v: %v", time.Since(start).Round(time.Millisecond), err)
	}
	t.Logf("Close took %v", time.Since(start).Round(time.Millisecond))
	if out := <-got; !bytes.Equal(out, in) {
		t.Fatalf("cat gave back %d of %d bytes", len(out), len(in))
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	paths := envd.Paths()
	last := slices.Index(paths, "CloseStdin")
	if last < 0 || slices.Contains(paths[last:], "SendInput") {
		t.Fatalf("calls = %v, want CloseStdin after the last SendInput", paths)
	}
}

// Input that stalls for good: Close gives up after the stall limit, says
// why, and sends no EOF.
func TestSendWriterCloseGivesUpWhenInputStalls(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.gate = make(chan struct{}) // no call ends
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if _, err := p.Stdin().Write(make([]byte, 3*chunk)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = p.Stdin().Close()
	if took := time.Since(start); took < 300*time.Millisecond || err == nil || !strings.Contains(err.Error(), "no SendInput call ended for 300ms") {
		t.Fatalf("Close after %v = %v, want the stall after 300ms", took.Round(time.Millisecond), err)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("a stalled input was closed")
	}
}

// Odd writes, each sent as a part of its own, so every part is odd, and so is
// the total: every byte arrives, in order.
func TestSendWriterKeepsEveryByteOfOddParts(t *testing.T) {
	e, envd := envdFor(t, false)
	gate := make(chan struct{})
	envd.mu.Lock()
	envd.gate = gate
	envd.mu.Unlock()
	file := filepath.Join(t.TempDir(), "in")
	p, err := e.Start(context.Background(), fmt.Sprintf("cat > %q", file))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, p.Stdout()) }()
	sizes := []int{13, chunk - 1, 7, 33333, 1}
	var in []byte
	for i, n := range sizes {
		part := make([]byte, n)
		for j := range part {
			part[j] = byte((len(in) + j) % 251)
		}
		in = append(in, part...)
		if _, err := p.Stdin().Write(part); err != nil {
			t.Fatal(err)
		}
		// The part waits alone in the queue while the call before it is
		// held. That call now ends, and the loop sends the part by itself.
		if i > 0 {
			release(t, gate)
		}
		waitFor(t, "the part's call", func() bool {
			envd.mu.Lock()
			defer envd.mu.Unlock()
			return len(envd.parts) == i+1
		})
	}
	release(t, gate)
	if err := p.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	envd.mu.Lock()
	parts := slices.Clone(envd.parts)
	envd.mu.Unlock()
	if !slices.Equal(parts, sizes) || len(in)%2 != 1 {
		t.Fatalf("parts = %v, want %v, with an odd total (%d)", parts, sizes, len(in))
	}
	if out, err := os.ReadFile(file); err != nil || !bytes.Equal(out, in) {
		t.Fatalf("cat wrote %d of %d bytes (equal: %v), %v", len(out), len(in), bytes.Equal(out, in), err)
	}
}

// release lets one held SendInput call end, or fails the test when no call
// waits for 30 s.
func release(t *testing.T, gate chan struct{}) {
	t.Helper()
	select {
	case gate <- struct{}{}:
	case <-time.After(30 * time.Second):
		t.Fatal("no SendInput call waited at the gate for 30s")
	}
}

// An EOF that cannot be sent is the input's failure: Close says so, as the
// command would wait for it for ever.
func TestSendWriterCloseReturnsAFailedEOF(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.failEOF = true
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "cat > /dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if _, err := p.Stdin().Write([]byte("all of the input")); err != nil {
		t.Fatal(err)
	}
	if err := p.Stdin().Close(); err == nil || !strings.Contains(err.Error(), "send the end of the input") || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Close = %v, want the failed EOF", err)
	}
}

// A CloseStdin call that never ends: the stall's error names it, though every
// SendInput call ended.
func TestSendWriterCloseNamesTheStalledCall(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.holdEOF = true
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "cat > /dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if _, err := p.Stdin().Write([]byte("all of the input")); err != nil {
		t.Fatal(err)
	}
	if err := p.Stdin().Close(); err == nil || !strings.Contains(err.Error(), "no CloseStdin call ended for 300ms") {
		t.Fatalf("Close = %v, want the stalled CloseStdin", err)
	}
}

// The loop ends as the stall's timer fires: Close never fails an input that
// was sent in full. Both are ready at once here, so a Close that took the
// timer without a second look at the loop would fail about half the time.
func TestSendWriterCloseNeverFailsASentInput(t *testing.T) {
	for range 200 {
		// A loop that has ended, as the loop leaves it.
		w := &sendWriter{done: make(chan struct{}), finished: true}
		w.cond = sync.NewCond(&w.mu)
		close(w.done)
		if err := w.Close(); err != nil {
			t.Fatalf("Close = %v, want nil for an input sent in full", err)
		}
	}
}

// A call that ends after Close's look at the progress, and before its
// decision, gives no stall: Close never decides from an old look. With one
// lock for the look and the decision, no call can end between them, and a
// stall is right.
func TestSendWriterCloseLooksAtTheLatestProgress(t *testing.T) {
	w := &sendWriter{stall: 100 * time.Millisecond, done: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	old := stallCheck
	t.Cleanup(func() { stallCheck = old })
	looks, moved := 0, false
	// The hook runs on Close's own goroutine, between its look at the
	// progress and its decision. Nothing else runs, so no timing decides
	// the test. The first look comes after a whole limit with no progress.
	// There the hook does what the send loop does when a call ends, if the
	// lock lets it in at once. A Close that takes that progress in looks
	// again a limit later, and by then the loop has ended.
	stallCheck = func() {
		looks++
		switch looks {
		case 1:
			if !w.mu.TryLock() {
				return // the look and the decision are one step
			}
			w.sentAt = time.Now()
			w.mu.Unlock()
			moved = true
		case 2:
			if w.mu.TryLock() {
				defer w.mu.Unlock()
			}
			w.finished = true
			close(w.done)
		}
	}
	err := w.Close()
	switch {
	case looks == 0:
		t.Fatal("Close made no decision")
	case moved && err != nil:
		t.Fatalf("Close = %v, want no stall: a call ended after its look, before its decision", err)
	case !moved && err == nil:
		t.Fatal("Close = nil, want a stall: no call ended before its decision")
	}
}

// A send loop with nothing to send ends with the command's context, so no
// goroutine is left when stdin is never closed.
func TestSendWriterLoopEndsWithCtx(t *testing.T) {
	e, _ := envdFor(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	p, err := e.Start(ctx, "cat > /dev/null")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, p.Stdout()) }()
	w := p.Stdin().(*sendWriter)
	cancel()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle send loop did not end when ctx ended")
	}
}
