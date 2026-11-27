package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"strconv"
	"time"
)

// start reads the history and starts a child that is fed it. It is used
// for the first child and for each restart.
func (v *VT) start() (returned error) {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return ErrVTDown
	}
	v.starts.Add(1)
	defer v.starts.Done()
	v.collecting = true
	v.pending = nil
	cols, rows := v.cols, v.rows
	v.mu.Unlock()

	var hist []byte
	var start int64
	if v.opt.History != nil {
		hist, start = v.opt.History()
	}
	end := start + int64(len(hist))
	c, err := v.spawn(cols, rows)

	v.mu.Lock()
	defer func() {
		v.mu.Unlock()
		if returned != nil && c != nil {
			c.stopUnstarted()
		}
	}()
	pending := v.pending
	v.collecting = false
	v.pending = nil
	if err != nil {
		return err
	}
	if v.closed {
		v.mu.Unlock()
		c.kill()
		<-c.gone
		v.mu.Lock()
		return ErrVTDown
	}
	if c.dead {
		return fmt.Errorf("the shadow terminal %s exited at once. Try: run it by hand to see why", v.opt.Path)
	}
	c.limit = v.maxBehind + len(hist)
	v.fed = max(v.fed, end)

	// The size and profile come first, so the history lays out as it did.
	if err := c.pushFrame(v.resizeFrame()); err != nil {
		return err
	}
	if v.profile != nil {
		if err := c.pushFrame(vtFrame(vtFrameProfile, v.profile)); err != nil {
			return err
		}
	}
	for p := hist; len(p) > 0; {
		n := min(len(p), vtChunk)
		if err := c.pushFrame(vtFrame(vtFrameData, p[:n])); err != nil {
			return err
		}
		p = p[n:]
	}
	// Then what arrived while the history was read, after its end.
	for _, ch := range pending {
		d := ch.data
		if ch.off < end {
			skip := end - ch.off
			if skip >= int64(len(d)) {
				continue
			}
			d = d[skip:]
		}
		if err := c.pushFrame(vtFrame(vtFrameData, d)); err != nil {
			return err
		}
	}
	// The history raised its events once already, so they are dropped
	// until the child has caught up. Its queries were answered then too,
	// so answering only starts after it.
	if len(hist) > 0 {
		c.muted = true
		c.waiters[vtFrameSync] = append(c.waiters[vtFrameSync], nil)
		if err := c.pushFrame(vtFrame(vtFrameSync, nil)); err != nil {
			return err
		}
	}
	if err := c.pushFrame(v.queriesFrame()); err != nil {
		return err
	}
	v.child = c
	return nil
}

// spawn starts the process and its goroutines.
func (v *VT) spawn(cols, rows int) (*vtChild, error) {
	// The sizes are capped already. The cap here is a guard.
	args := []string{"--cols", strconv.Itoa(int(clampCols(cols))), "--rows", strconv.Itoa(int(clampRows(rows)))}
	if v.opt.Scrollback > 0 {
		args = append(args, "--scrollback", strconv.Itoa(v.opt.Scrollback))
	}
	cmd := exec.CommandContext(context.Background(), v.opt.Path, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("connect to the shadow terminal's input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("connect to the shadow terminal's output: %w", err)
	}
	if v.opt.Log != nil {
		cmd.Stderr = &vtLogWriter{log: v.opt.Log}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the shadow terminal %s: %w. Try: reinstall the host helper", v.opt.Path, err)
	}
	c := &vtChild{
		cmd:        cmd,
		stdin:      stdin,
		started:    time.Now(),
		wake:       make(chan struct{}, 1),
		gone:       make(chan struct{}),
		writerDone: make(chan struct{}),
		readerDone: make(chan struct{}),
		waiters:    map[byte][]chan vtReply{},
	}
	go v.writeLoop(c)
	go v.readLoop(c, stdout)
	return c, nil
}

// writeLoop sends queued frames to the child's stdin.
func (v *VT) writeLoop(c *vtChild) {
	defer close(c.writerDone)
	w := bufio.NewWriterSize(c.stdin, 64<<10)
	for {
		v.mu.Lock()
		for len(c.queue) == 0 && !c.dead {
			v.mu.Unlock()
			select {
			case <-c.wake:
			case <-c.gone:
				return
			}
			v.mu.Lock()
		}
		if c.dead {
			v.mu.Unlock()
			return
		}
		batch := c.queue
		c.queue = nil
		v.mu.Unlock()

		n := 0
		for _, f := range batch {
			if _, err := w.Write(f); err != nil {
				c.kill()
				return
			}
			n += len(f)
		}
		if err := w.Flush(); err != nil {
			c.kill()
			return
		}
		v.mu.Lock()
		c.queued -= n
		v.mu.Unlock()
	}
}

// readLoop handles the child's frames until it exits, then restarts it.
func (v *VT) readLoop(c *vtChild, stdout io.Reader) {
	defer close(c.readerDone)
	r := bufio.NewReaderSize(stdout, 64<<10)
	var err error
	for {
		var hdr [5]byte
		if _, err = io.ReadFull(r, hdr[:]); err != nil {
			break
		}
		n := binary.LittleEndian.Uint32(hdr[1:])
		p := make([]byte, n)
		if _, err = io.ReadFull(r, p); err != nil {
			break
		}
		v.handle(c, hdr[0], p)
	}
	werr := c.cmd.Wait()
	if werr != nil {
		err = werr
	}
	v.exited(c, err)
}

func (v *VT) handle(c *vtChild, kind byte, p []byte) {
	switch kind {
	case vtFrameWrite:
		if f := v.opt.OnWrite; f != nil {
			f(p)
		}
	case vtFrameEvent:
		v.mu.Lock()
		muted := c.muted
		v.mu.Unlock()
		if f := v.opt.OnEvent; f != nil && !muted {
			var e VTEvent
			if json.Unmarshal(p, &e) == nil {
				f(e)
			}
		}
	case vtFrameSnapshot, vtFrameDump, vtFrameSync:
		v.mu.Lock()
		q := c.waiters[kind]
		var ch chan vtReply
		if len(q) > 0 {
			ch = q[0]
			c.waiters[kind] = q[1:]
		}
		if ch == nil && kind == vtFrameSync {
			// The history replay has finished.
			c.muted = false
		}
		v.mu.Unlock()
		if ch != nil {
			ch <- vtReply{p: p}
		}
	}
}

// exited runs once the child's process has exited.
func (v *VT) exited(c *vtChild, err error) {
	v.mu.Lock()
	c.dead = true
	close(c.gone)
	c.fail(ErrVTDown)
	if v.child != c || v.closed {
		v.mu.Unlock()
		return
	}
	v.child = nil
	if time.Since(c.started) > vtStableAfter {
		v.crashes = 0
	}
	wait := vtBackoff[min(v.crashes, len(vtBackoff)-1)]
	v.crashes++
	v.mu.Unlock()

	if err == nil {
		err = errors.New("it closed its output")
	}
	v.log.Warn("shadow terminal stopped, restarting it", "err", err, "wait", wait)
	go v.restart(wait)
}

func (v *VT) restart(wait time.Duration) {
	for {
		select {
		case <-v.done:
			return
		case <-time.After(wait):
		}
		err := v.start()
		if err == nil || errors.Is(err, ErrVTDown) {
			return
		}
		v.mu.Lock()
		wait = vtBackoff[min(v.crashes, len(vtBackoff)-1)]
		v.crashes++
		v.mu.Unlock()
		v.log.Warn("shadow terminal did not start, trying again", "err", err, "wait", wait)
	}
}

func (v *VT) resizeFrame() ([]byte, error) {
	var p [8]byte
	binary.LittleEndian.PutUint16(p[0:], clampCols(v.cols))
	binary.LittleEndian.PutUint16(p[2:], clampRows(v.rows))
	binary.LittleEndian.PutUint16(p[4:], clamp16(v.xpx))
	binary.LittleEndian.PutUint16(p[6:], clamp16(v.ypx))
	return vtFrame(vtFrameResize, p[:])
}

func (v *VT) queriesFrame() ([]byte, error) {
	on := byte(0)
	if v.answering {
		on = 1
	}
	return vtFrame(vtFrameQueries, []byte{on})
}

// pushFrame refuses an encoding error before queueing. VT.mu is held.
func (c *vtChild) pushFrame(f []byte, err error) error {
	if err != nil {
		c.kill()
		return err
	}
	c.push(f)
	return nil
}

// push queues a frame for the writer. VT.mu is held.
func (c *vtChild) push(f []byte) {
	if c.dead {
		return
	}
	c.queue = append(c.queue, f)
	c.queued += len(f)
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// fail ends every request still waiting for a reply. VT.mu is held.
func (c *vtChild) fail(err error) {
	for k, q := range c.waiters {
		for _, ch := range q {
			if ch != nil {
				ch <- vtReply{err: err}
			}
		}
		delete(c.waiters, k)
	}
}

func (c *vtChild) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// stopUnstarted joins both workers after a refused startup. VT.mu is not held.
func (c *vtChild) stopUnstarted() {
	_ = c.stdin.Close()
	c.kill()
	<-c.gone // the process is reaped; readerDone joins waiter cleanup
	<-c.readerDone
	<-c.writerDone
}

// vtFrameSize checks the wire length and header arithmetic before allocation.
func vtFrameSize(n int) (int, error) {
	if n < 0 || uint64(n) > math.MaxUint32 || n > math.MaxInt-5 {
		return 0, fmt.Errorf("shadow terminal payload size %d cannot fit a frame. Try: use a smaller terminal profile", n)
	}
	return 5 + n, nil
}

// vtFrame encodes one frame without truncating its wire length.
func vtFrame(kind byte, payload []byte) ([]byte, error) {
	size, err := vtFrameSize(len(payload))
	if err != nil {
		return nil, err
	}
	f := make([]byte, size)
	f[0] = kind
	binary.LittleEndian.PutUint32(f[1:], uint32(len(payload))) //nolint:gosec // G115: vtFrameSize checked the uint32 wire length and host int bound.
	copy(f[5:], payload)
	return f, nil
}

// clamp16 is n as a uint16: a negative n is 0, and a larger one the largest.
// It is for pixel sizes. Columns and rows use clampCols and clampRows.
func clamp16(n int) uint16 {
	return uint16(min(max(n, 0), 0xffff))
}

// clamp32 is n as a uint32: a negative n is 0, and a larger one the largest.
func clamp32(n int) uint32 {
	return uint32(min(max(int64(n), 0), math.MaxUint32))
}

// vtLogWriter passes the child's stderr to the log, a line at a time.
type vtLogWriter struct {
	log *slog.Logger
	buf []byte
}

func (w *vtLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.log.Info("shadow terminal stderr", "line", string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4096 {
		w.log.Info("shadow terminal stderr", "line", string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}
