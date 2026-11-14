package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// streamTransit is the most input that the far end and the path are assumed to
// hold once the transport has read the end of a StreamInput request's body: up
// to 1 MiB at the far end (the HTTP/2 stream window in envd's Go server, or
// the socket buffer over HTTP/1.1) plus 64 KiB on the path. A larger buffer
// makes Close give up early, safely: it sends no EOF, and Run reports no
// success.
const streamTransit = 1<<20 + 64<<10

// streamTransitWait is how long streamTransit may take to reach envd at
// 100 KiB/s, about 11 s. After the transport has read the body's EOF, Close
// waits the stall limit plus this time for envd's answer. Tests shorten it.
var streamTransitWait = time.Duration(streamTransit) * time.Second / (100 << 10)

// watchHook, when set, runs in watch after its timer is made and before it
// waits. A test uses it to hold a stopped watchdog until its timer has fired.
var watchHook func(stop <-chan struct{})

// streamWriter carries stdin in one StreamInput request: one envelope for
// each write, in order, over a single long request. StreamInput uses the
// client's own transport, unchanged.
//
// Progress during the stream is the transport's reads of the request body
// (the pipe). Flow control over HTTP/2, and the socket buffers over HTTP/1.1,
// let the transport read only as fast as the far end takes the input, up to
// what the far end and the path hold. Close, and a Write that waits, give up
// when the transport reads nothing from the pipe for the stall limit. After
// the transport has read the body's EOF, Close waits the stall limit plus
// streamTransitWait for envd's answer, assuming the far end and the path hold
// at most 1 MiB plus 64 KiB. A larger buffer makes Close give up early,
// safely: it sends no EOF, and Run reports no success.
type streamWriter struct {
	p *proc
	// ctx lives as long as the process, and ends when the process is closed.
	ctx   context.Context
	stall time.Duration
	pr    *io.PipeReader
	pw    *io.PipeWriter
	mu    sync.Mutex
	err   error
	// movedAt is the request's last progress: a read of the pipe by the
	// transport, or its read of the pipe's EOF. eof is set once the
	// transport has read the end of the body.
	movedAt time.Time
	eof     bool
	// closing is set when Close starts, and ended when the request has
	// ended.
	closing bool
	ended   bool
	done    chan struct{}
}

func newStreamWriter(ctx context.Context, p *proc) *streamWriter {
	pr, pw := io.Pipe()
	w := &streamWriter{p: p, ctx: ctx, stall: stallLimit, pr: pr, pw: pw, done: make(chan struct{})}
	go w.run()
	first := streamInputRequest{Start: &struct {
		Process selector `json:"process"`
	}{Process: selector{Pid: p.pid}}}
	if err := w.send(first); err != nil {
		w.setErr(err)
	}
	return w
}

// moving is the request's body. It notes each read of the pipe, and its EOF.
type moving struct {
	r io.Reader
	w *streamWriter
}

func (m moving) Read(b []byte) (int, error) {
	n, err := m.r.Read(b)
	m.w.mu.Lock()
	if n > 0 || err == io.EOF {
		m.w.movedAt = time.Now()
	}
	if err == io.EOF {
		m.w.eof = true
	}
	m.w.mu.Unlock()
	return n, err //nolint:wrapcheck // the body passes the pipe's errors on as they are
}

func (w *streamWriter) run() {
	defer close(w.done)
	stop := context.AfterFunc(w.ctx, func() {
		_ = w.pr.CloseWithError(w.ctx.Err())
	})
	defer stop()
	defer func() {
		w.mu.Lock()
		w.ended = true
		err := w.err
		w.mu.Unlock()
		// A write after the end fails at once instead of waiting for a
		// request that takes no more.
		if err == nil {
			err = io.ErrClosedPipe
		}
		_ = w.pr.CloseWithError(err)
	}()
	req, err := w.p.e.request(w.ctx, "StreamInput", moving{r: w.pr, w: w}, true)
	if err != nil {
		w.setErr(err)
		return
	}
	resp, err := w.p.e.client().Do(req)
	if err != nil {
		w.setErr(fmt.Errorf("envd StreamInput: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.setErr(fmt.Errorf("envd StreamInput: %w", responseError(resp)))
		return
	}
	for {
		flags, msg, err := readEnvelope(resp.Body)
		if err != nil {
			w.setErr(fmt.Errorf("envd StreamInput: the stream broke before the end of the input: %w", err))
			return
		}
		if flags&flagEndStream != 0 {
			if err := endError(msg); err != nil {
				w.setErr(err)
			}
			w.mu.Lock()
			early := !w.closing
			w.mu.Unlock()
			if early {
				w.setErr(errors.New("envd StreamInput: envd ended the stream before the end of the input"))
			}
			return
		}
	}
}

func (w *streamWriter) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
}

// left is how long the request may still go without progress, counted from
// since or from its last progress, whichever is later. w.mu is held.
func (w *streamWriter) left(since time.Time) time.Duration {
	last := w.movedAt
	if last.Before(since) {
		last = since
	}
	return w.limit() - time.Since(last)
}

// limit is the stall limit, or, once the transport has read the body's EOF,
// the stall limit plus the time for what may still be on its way. w.mu is
// held.
func (w *streamWriter) limit() time.Duration {
	if w.eof {
		return w.stall + streamTransitWait
	}
	return w.stall
}

// stalled fails the request for the stall that left found. w.mu is held.
func (w *streamWriter) stalled() error {
	if w.err == nil {
		if w.eof {
			w.err = fmt.Errorf("stdin was not sent in time: envd did not answer the StreamInput request for %v after its last byte", w.limit())
		} else {
			w.err = fmt.Errorf("stdin was not sent in time: the StreamInput request took no input for %v", w.stall)
		}
	}
	return w.err
}

// watch guards one write of the pipe: when the transport reads nothing from
// the pipe for the stall limit, it fails the request, and so the write. The
// func that it returns stops the watch and waits for it to finish, so a
// stopped watch never fires later.
func (w *streamWriter) watch() func() {
	since := time.Now()
	stop := make(chan struct{})
	done := make(chan struct{})
	stopped := false
	go func() {
		defer close(done)
		t := time.NewTimer(w.stall)
		defer t.Stop()
		if watchHook != nil {
			watchHook(stop)
		}
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			w.mu.Lock()
			if stopped {
				w.mu.Unlock()
				return
			}
			left := w.left(since)
			if left > 0 {
				w.mu.Unlock()
				t.Reset(left)
				continue
			}
			err := w.stalled()
			w.mu.Unlock()
			_ = w.pr.CloseWithError(err)
			return
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			stopped = true
			w.mu.Unlock()
			close(stop)
			<-done
		})
	}
}

func (w *streamWriter) send(m streamInputRequest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("envd StreamInput: encode the input: %w", err)
	}
	var buf bytes.Buffer
	if err := writeEnvelope(&buf, 0, b); err != nil {
		return err
	}
	stop := w.watch()
	_, err = w.pw.Write(buf.Bytes())
	stop()
	if err == nil {
		w.mu.Lock()
		err = w.err
		w.mu.Unlock()
	}
	if err != nil {
		return fmt.Errorf("envd StreamInput: %w", err)
	}
	return nil
}

func (w *streamWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return 0, err
	}
	for off := 0; off < len(b); off += chunk {
		end := min(off+chunk, len(b))
		m := streamInputRequest{Data: &struct {
			Input processInput `json:"input"`
		}{Input: processInput{Stdin: b[off:end]}}}
		if err := w.send(m); err != nil {
			return off, err
		}
	}
	return len(b), nil
}

// Close ends the input stream, then sends EOF to the command, only when the
// stream ended well, so the command never takes part of its input for all of
// it. During the stream it waits while the transport reads the pipe, and gives
// up when the transport reads nothing for the stall limit. After the transport
// has read the body's EOF, it waits the stall limit plus streamTransitWait for
// envd's answer, assuming at most streamTransit is on the way. A larger buffer
// makes Close give up early, safely, with no EOF.
func (w *streamWriter) Close() error {
	w.mu.Lock()
	w.closing = true
	w.mu.Unlock()
	_ = w.pw.Close()
	since := time.Now()
	t := time.NewTimer(w.stall)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return w.sendEOF()
		case <-t.C:
		}
		w.mu.Lock()
		left := w.left(since)
		if left > 0 || w.ended {
			w.mu.Unlock()
			if left > 0 {
				t.Reset(left)
			}
			continue
		}
		err := w.stalled()
		w.mu.Unlock()
		_ = w.pr.CloseWithError(err)
		return err
	}
}

// sendEOF sends CloseStdin, once the stream has ended well.
func (w *streamWriter) sendEOF() error {
	w.mu.Lock()
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	if err := w.p.e.unary(ctx, "CloseStdin", map[string]selector{"process": {Pid: w.p.pid}}); err != nil {
		return fmt.Errorf("send the end of the input: %w", err)
	}
	return nil
}
