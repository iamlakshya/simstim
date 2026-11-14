package e2b

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// maxPending is how much stdin may wait to be sent before Write blocks.
const maxPending = 1 << 20

// stallLimit is how long Close waits for the next SendInput call to end. A
// slow link that still moves the input never reaches it. Tests shorten it.
var stallLimit = 10 * time.Second

// stallCheck, when set, runs in Close between its look at the progress and
// its decision. A test uses it to try to make progress there.
var stallCheck func()

// sendWriter carries stdin in SendInput calls, one at a time and in order,
// as the E2B SDKs and CLI do. Write only queues the bytes, so keys typed
// while a call is in flight go together in the next one.
type sendWriter struct {
	p     *proc
	stall time.Duration

	mu       sync.Mutex
	cond     *sync.Cond
	buf      []byte
	closed   bool
	err      error
	sentAt   time.Time // when the last SendInput call ended
	call     string    // the call in flight: SendInput or CloseStdin
	finished bool      // the loop has ended
	done     chan struct{}
}

func newSendWriter(ctx context.Context, p *proc) *sendWriter {
	w := &sendWriter{p: p, stall: stallLimit, done: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	go w.loop(ctx)
	return w
}

// Write queues b in parts, so no more than maxPending waits at any time. A
// large write, such as the whole helper in an upload, then leaves Close only
// the last part to send.
func (w *sendWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for {
		for len(w.buf) >= maxPending && w.err == nil && !w.closed {
			w.cond.Wait()
		}
		if w.err != nil {
			return n, w.err
		}
		if w.closed {
			return n, io.ErrClosedPipe
		}
		k := min(len(b)-n, maxPending-len(w.buf))
		w.buf = append(w.buf, b[n:n+k]...)
		n += k
		w.cond.Broadcast()
		if n == len(b) {
			return n, nil
		}
	}
}

// next waits for bytes to send. It returns nil when stdin is closed and
// everything is sent, or when sending failed.
func (w *sendWriter) next() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.buf) == 0 && !w.closed && w.err == nil {
		w.cond.Wait()
	}
	if w.err != nil || len(w.buf) == 0 {
		return nil
	}
	n := min(len(w.buf), chunk)
	out := append([]byte(nil), w.buf[:n]...)
	w.buf = append(w.buf[:0], w.buf[n:]...)
	w.cond.Broadcast()
	return out
}

// loop sends the input as it comes. ctx lives as long as the process: its end
// ends the loop, and a Write or Close that waits.
func (w *sendWriter) loop(ctx context.Context) {
	defer close(w.done)
	defer func() {
		w.mu.Lock()
		w.finished = true
		w.mu.Unlock()
	}()
	stop := context.AfterFunc(ctx, func() { w.fail(context.Cause(ctx)) })
	defer stop()
	for {
		b := w.next()
		if b == nil {
			break
		}
		w.setCall("SendInput")
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := w.p.e.unary(sctx, "SendInput", sendInputRequest{Process: selector{Pid: w.p.pid}, Input: processInput{Stdin: b}})
		cancel()
		if err != nil {
			w.fail(err)
			return
		}
		w.mu.Lock()
		w.sentAt = time.Now()
		w.mu.Unlock()
	}
	w.mu.Lock()
	failed := w.err != nil
	w.mu.Unlock()
	if !failed {
		w.setCall("CloseStdin")
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// The command waits for this EOF, so a failure to send it is the
		// input's failure too.
		if err := w.p.e.unary(sctx, "CloseStdin", map[string]selector{"process": {Pid: w.p.pid}}); err != nil {
			w.fail(fmt.Errorf("send the end of the input: %w", err))
		}
	}
}

func (w *sendWriter) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
	w.cond.Broadcast()
}

// Close sends what is queued, then EOF. It waits as long as the input moves:
// it gives up only when no SendInput or CloseStdin call ends for w.stall, or
// when the command's context ends. Its error names the call that stalled.
func (w *sendWriter) Close() error {
	w.mu.Lock()
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()
	since := time.Now()
	t := time.NewTimer(w.stall)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return w.result()
		case <-t.C:
		}
		// The look at the progress and the decision are one step, under the
		// lock that the loop takes for each call that ends, and when it ends.
		// So a stall is never made from an old look, and an input sent in
		// full never fails.
		w.mu.Lock()
		last := w.sentAt
		if last.Before(since) {
			last = since
		}
		if stallCheck != nil {
			stallCheck()
		}
		left := w.stall - time.Since(last)
		if left > 0 || w.finished {
			w.mu.Unlock()
			if left <= 0 {
				return w.result()
			}
			t.Reset(left)
			continue
		}
		if w.err == nil {
			w.err = fmt.Errorf("stdin was not sent in time: no %s call ended for %v", w.call, w.stall)
		}
		w.cond.Broadcast()
		err := w.err
		w.mu.Unlock()
		return err
	}
}

// setCall names the call that the loop makes now, for a stall's error.
func (w *sendWriter) setCall(name string) {
	w.mu.Lock()
	w.call = name
	w.mu.Unlock()
}

func (w *sendWriter) result() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
