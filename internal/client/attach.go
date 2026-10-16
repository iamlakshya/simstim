package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/muesli/cancelreader"
	xterm "golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/keys"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
)

// AttachState is what the client remembers about the last session attached in
// this terminal, so the next attach can continue from the exact byte the
// terminal already shows instead of redrawing everything.
type AttachState struct {
	Key    string // host/session
	Offset int64
	Modes  term.Modes
	Valid  bool
}

// AttachOptions configures one attach.
type AttachOptions struct {
	Host     Host
	Session  string
	Settings Settings
	State    *AttachState
	// Interactive allows SSH prompts on the first connection.
	Interactive bool
	In, Out     *os.File
}

// AttachResult says how an attach ended.
type AttachResult struct {
	Detached   bool
	Ended      *proto.Ended
	Missed     int64 // bytes replayed after reconnects
	Reconnects int
}

// Heartbeat timings.
const (
	pingEvery   = 5 * time.Second
	deadAfter   = 15 * time.Second
	bannerAfter = 3 * time.Second
	doubleTap   = 400 * time.Millisecond
	watchEvery  = 5 * time.Second
	wakeGap     = 4 * time.Second
)

// escFlush is how long the input filter may hold an unfinished key sequence
// before it goes to the agent as it is. A lone Esc never waits. Tests change it.
var escFlush = 15 * time.Millisecond

type attacher struct {
	o       AttachOptions
	conn    *Conn
	ssh     *SSH
	fd      int
	tracker *term.Tracker
	cu      term.Catchup // where the terminal is in the session's output
	filter  *keys.Filter
	inCh    chan []byte
	res     AttachResult
	key     string
	// swallow holds watched keys whose press we consumed, so their release is swallowed too.
	swallow map[int]bool
	// seen is the last known state of each session on this host, for notifications.
	seen map[string]string
}

// Attach connects to a session and runs it in this terminal until the user
// detaches, the session ends, or ctx is cancelled. The session keeps running.
func Attach(ctx context.Context, o AttachOptions) (AttachResult, error) {
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.State == nil {
		o.State = &AttachState{}
	}
	dk, err := keys.Parse(o.Settings.Attach.DetachKey)
	if err != nil {
		return AttachResult{}, &UserError{Msg: "bad detach key: " + err.Error(), Hint: "Fix attach.detach_key in the config."}
	}
	pk, err := keys.Parse(o.Settings.Attach.ImagePasteKey)
	if err != nil {
		return AttachResult{}, &UserError{Msg: "bad image paste key: " + err.Error(), Hint: "Fix attach.image_paste_key in the config."}
	}
	a := &attacher{o: o, ssh: &SSH{Host: o.Host}, fd: int(o.In.Fd()), filter: keys.New(dk, pk),
		key: o.Host.Name + "/" + o.Session, swallow: map[int]bool{}}

	last := int64(-1)
	if o.State.Valid && o.State.Key == a.key {
		last = o.State.Offset
	}
	conn, err := Dial(ctx, o.Host, o.Interactive)
	if err != nil {
		return AttachResult{}, err
	}
	a.conn = conn
	defer func() { a.conn.Close() }()
	cols, rows, xpx, ypx := a.size()
	att, err := a.handshake(last, cols, rows, xpx, ypx)
	if err != nil {
		return AttachResult{}, err
	}

	old, err := xterm.MakeRaw(a.fd)
	if err != nil {
		return AttachResult{}, fmt.Errorf("cannot put the terminal in raw mode: %w", err)
	}
	cr, err := cancelreader.NewReader(o.In)
	if err != nil {
		_ = xterm.Restore(a.fd, old)
		return AttachResult{}, fmt.Errorf("read the terminal: %w", err)
	}
	a.inCh = make(chan []byte, 256)
	stop, readDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := cr.Read(buf)
			if n > 0 {
				select {
				case a.inCh <- append([]byte(nil), buf[:n]...):
				case <-stop:
					return
				}
			}
			if err != nil {
				close(a.inCh)
				return
			}
		}
	}()

	// Remember the title, to put it back on detach. While attached the
	// title is the agent's: the client draws nothing of its own.
	a.write([]byte("\x1b[22;0t"))
	a.begin(att, o.State, rows)

	runErr := a.loop(ctx)

	// Stop the reader before its files are closed, and before anything else
	// reads the terminal.
	cr.Cancel()
	close(stop)
	select {
	case <-readDone:
	case <-time.After(time.Second):
	}
	// Put the terminal back exactly as we found it: undo the session's modes,
	// never with RIS, which would wipe the scrollback.
	a.write(a.tracker.M.ResetSequence())
	a.write([]byte("\x1b]9;4;0\x1b\\\x1b[23;0t"))
	_ = xterm.Restore(a.fd, old)
	_ = cr.Close()

	o.State.Key, o.State.Offset, o.State.Modes, o.State.Valid = a.key, a.cu.Offset, a.tracker.M, true
	if a.res.Ended != nil {
		o.State.Valid = false
	}
	return a.res, runErr
}

func (a *attacher) write(b []byte) {
	if len(b) > 0 {
		_, _ = a.o.Out.Write(b)
	}
}

func (a *attacher) size() (cols, rows, xpx, ypx int) { return termSize(a.o.Out) }

// handshake sends the attach request and waits for the answer.
func (a *attacher) handshake(last int64, cols, rows, xpx, ypx int) (proto.Attached, error) {
	req := proto.Attach{Session: a.o.Session, Cols: cols, Rows: rows, XPx: xpx, YPx: ypx, Client: brand.Name, LastOffset: last}
	if err := a.conn.SendJSON(proto.TAttach, req); err != nil {
		return proto.Attached{}, ErrClosed
	}
	timeout := time.After(15 * time.Second)
	for {
		select {
		case f := <-a.conn.Stream():
			switch f.Type {
			case proto.TAttached:
				var att proto.Attached
				if err := json.Unmarshal(f.Payload, &att); err != nil {
					return att, fmt.Errorf("read the host's answer to attach: %w", err)
				}
				return att, nil
			case proto.TError:
				var pe proto.Error
				_ = json.Unmarshal(f.Payload, &pe)
				return proto.Attached{}, &UserError{Msg: pe.Message, Hint: pe.Hint}
			default:
				// Other frames before the answer are ignored.
			}
		case <-a.conn.Done():
			return proto.Attached{}, ErrClosed
		case <-timeout:
			return proto.Attached{}, errors.New("the host did not answer the attach in time")
		}
	}
}

// begin prepares the screen for the bytes that follow.
func (a *attacher) begin(att proto.Attached, st *AttachState, rows int) {
	a.cu.History(att.Live)
	if att.Mode == "replay" && st.Valid && st.Key == a.key {
		// The terminal still shows this session up to st.Offset: put its modes back and continue.
		a.tracker = &term.Tracker{M: st.Modes}
		a.cu.Offset = att.From
		a.write(st.Modes.RestoreSequence())
		return
	}
	// A full redraw. Keep what is on screen in the scrollback, then start clean.
	a.tracker = term.NewTracker()
	a.cu.Offset = att.From
	// For "snapshot", a TSnapshot frame redraws the screen as of From, and the
	// output from From follows; bytes before Live are history and are sanitised.
	if a.o.Settings.Attach.Scrollback == "clear" {
		a.write([]byte("\x1b[H\x1b[2J\x1b[3J"))
	} else {
		a.write([]byte(fmt.Sprintf("\x1b[%d;1H%s\x1b[H", rows, strings.Repeat("\n", rows))))
	}
}

// output shows bytes from offset off, skipping any the terminal already has.
func (a *attacher) output(off int64, data []byte) {
	show, fresh := a.cu.Feed(off, data)
	if fresh == nil {
		return // the terminal already shows it
	}
	a.write(show)
	a.tracker.Feed(fresh)
}

func (a *attacher) loop(ctx context.Context) error {
	winch, stopWinch := watchResize(a.o.Out)
	defer stopWinch()
	heart := time.NewTicker(pingEvery)
	defer heart.Stop()
	watch := time.NewTicker(watchEvery)
	defer watch.Stop()
	// A gap in this ticker means the process was frozen, as by laptop sleep:
	// the connection is almost surely dead, so reconnect at once rather than
	// wait for the heartbeat to time out.
	wake := time.NewTicker(time.Second)
	defer wake.Stop()
	lastTick := time.Now()
	others := make(chan []Session, 1)
	var detachTimer, flushTimer <-chan time.Time
	for {
		select {
		case now := <-wake.C:
			gap := now.Sub(lastTick)
			lastTick = now
			if gap > wakeGap {
				a.conn.kill()
			}
		case <-watch.C:
			a.watchOthers(ctx, others)
		case list := <-others:
			a.notifyOthers(list)
		case f := <-a.conn.Stream():
			if done, err := a.frame(f); done || err != nil {
				return err
			}
		case b, ok := <-a.inCh:
			if a.onKeys(ctx, b, ok, &detachTimer, &flushTimer) {
				return nil
			}
		case <-flushTimer:
			flushTimer = nil
			if b := a.filter.Flush(); len(b) > 0 {
				_ = a.conn.Send(proto.TInput, b)
			}
		case <-detachTimer:
			a.res.Detached = true
			return nil
		case <-winch:
			c, r, x, y := a.size()
			_ = a.conn.SendJSON(proto.TResize, proto.Resize{Cols: c, Rows: r, XPx: x, YPx: y})
		case <-heart.C:
			a.heartbeat()
		case <-a.conn.Done():
			if detached, err := a.reconnect(ctx); detached || err != nil {
				a.res.Detached = detached
				return err
			}
		case <-ctx.Done():
			a.res.Detached = true
			return nil
		}
	}
}

// watchOthers asks the host for its sessions in the background, so the
// loop can notify about the others; a slow answer is dropped.
func (a *attacher) watchOthers(ctx context.Context, others chan<- []Session) {
	conn := a.conn
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if list, err := conn.Sessions(ctx); err == nil {
			select {
			case others <- list:
			default:
			}
		}
	}()
}

// onKeys handles bytes from the local terminal; ok is false when it closed.
// It returns true when the attach is over.
func (a *attacher) onKeys(ctx context.Context, b []byte, ok bool, detachTimer, flushTimer *<-chan time.Time) bool {
	if !ok {
		a.res.Detached = true
		return true
	}
	if a.input(ctx, b, detachTimer) {
		return true
	}
	if a.filter.Pending() {
		*flushTimer = time.After(escFlush)
	}
	return false
}

// heartbeat pings the host, and drops a connection that stopped answering.
func (a *attacher) heartbeat() {
	_ = a.conn.Ping()
	if time.Since(a.conn.LastRecv()) > deadAfter {
		a.conn.kill()
	}
}

// frame handles one frame from the host. done is true when the attach is over.
func (a *attacher) frame(f proto.Frame) (done bool, err error) {
	switch f.Type {
	case proto.TOutput:
		off, data, err := proto.DecodeOutput(f.Payload)
		if err == nil {
			a.output(off, data)
		}
	case proto.TSnapshot:
		// A redraw of the current screen, made by the host's terminal emulator.
		// It contains no queries, so it goes straight to the terminal.
		a.write(f.Payload)
		a.tracker.Feed(f.Payload)
	case proto.TEnded:
		var e proto.Ended
		_ = json.Unmarshal(f.Payload, &e)
		a.res.Ended = &e
		return true, nil
	case proto.TError:
		var pe proto.Error
		_ = json.Unmarshal(f.Payload, &pe)
		if pe.Code == "session_gone" {
			a.res.Ended = &proto.Ended{ExitCode: -1}
			return true, nil
		}
		return true, &UserError{Msg: pe.Message, Hint: pe.Hint}
	default:
		// Other frames need nothing from an attached client.
	}
	return false, nil
}

// input handles keys typed by the user. It returns true to detach now.
func (a *attacher) input(ctx context.Context, b []byte, detachTimer *<-chan time.Time) bool {
	for _, p := range a.filter.Feed(b) {
		switch p.Kind {
		case keys.Pass:
			_ = a.conn.Send(proto.TInput, p.Raw)
		case keys.Paste:
			a.paste(ctx, p)
		case keys.Match:
			switch p.Index {
			case 0:
				a.detachKey(p, detachTimer)
			case 1:
				a.imageKey(ctx, p)
			}
		}
	}
	return false
}

// detachKey handles the detach key. One press arms a short timer, and the
// attach ends when it fires; a second press within it sends the key itself
// to the agent. A release or repeat follows its press: swallowed or sent.
func (a *attacher) detachKey(p keys.Piece, detachTimer *<-chan time.Time) {
	if p.Release || p.Repeat {
		if !a.swallow[0] {
			_ = a.conn.Send(proto.TInput, p.Raw)
		}
		return
	}
	if *detachTimer != nil {
		// A second press: send the key itself to the agent.
		*detachTimer = nil
		a.swallow[0] = false
		_ = a.conn.Send(proto.TInput, p.Raw)
		return
	}
	a.swallow[0] = true
	*detachTimer = time.After(doubleTap)
}

// imageKey handles the image paste key: with an image on the clipboard it
// uploads it, and otherwise the key goes to the agent as it is.
func (a *attacher) imageKey(ctx context.Context, p keys.Piece) {
	if p.Release || p.Repeat {
		if !a.swallow[1] {
			_ = a.conn.Send(proto.TInput, p.Raw)
		}
		return
	}
	img := ClipboardImage(ctx)
	if img == nil {
		a.swallow[1] = false
		_ = a.conn.Send(proto.TInput, p.Raw)
		return
	}
	a.swallow[1] = true
	a.sendUpload(ctx, "clipboard.png", img)
}
