package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	xterm "golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/keys"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
)

// AttachState is what the client remembers about the last session attached in
// this terminal, so the next attach can continue from the exact byte the
// terminal already shows instead of redrawing everything. A session that was
// on the alternate screen is drawn again: detach left that screen, and the
// terminal does not keep it.
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
	Detached    bool
	Ended       *proto.Ended
	Missed      int64 // bytes replayed after reconnects
	Reconnects  int
	ReaderStuck bool
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
	agent         *attachedAgent
	agentReady    <-chan agentReady
	agentStarting bool
	agentSetup    sync.WaitGroup
	inputPending  bool
	o             AttachOptions
	conn          *Conn
	ssh           *SSH
	fd            int
	tracker       *term.Tracker
	cu            term.Catchup // where the terminal is in the session's output
	filter        *keys.Filter
	in            *attachInput
	inCh          chan []byte
	res           AttachResult
	key           string
	// swallow holds watched keys whose press we consumed, so their release is swallowed too.
	swallow map[int]bool
	// seen is the last known state of each session on this host, for notifications.
	seen map[string]string
	// outStream tracks ground boundaries in TOutput so notifications never
	// split an unfinished escape sequence or multi-byte UTF-8 rune.
	outStream     outStream
	pendingNotify [][]byte
	tr            *attachTrace // the debug trace (trace.go), or nil
	// dropped says why the attach dropped its connection, for the debug
	// mode: "" when the connection closed by itself.
	dropped string
	guard   *pasteGuard  // reads every byte to the terminal for clipboard writes, and checks drops
	held    []heldNotice // notices that wait until the terminal is between sequences
	// clipUnread says that the paste guard could not read the clipboard in
	// this attach, which its debug log said once.
	clipUnread bool
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
	if err := checkAttachInput(o.In); err != nil {
		return AttachResult{}, err
	}
	if o.State == nil {
		o.State = &AttachState{}
	}
	dk, err := keys.Parse(o.Settings.Attach.DetachKey)
	if err != nil {
		return AttachResult{}, &UserError{Msg: "bad detach key: " + err.Error(), Hint: "Fix attach.detach_key in the config.", Code: "bad_config", Kind: KindUsage}
	}
	pk, err := keys.Parse(o.Settings.Attach.ImagePasteKey)
	if err != nil {
		return AttachResult{}, &UserError{Msg: "bad image paste key: " + err.Error(), Hint: "Fix attach.image_paste_key in the config.", Code: "bad_config", Kind: KindUsage}
	}
	tr := openAttachTrace(o.Session)
	ctx = withTrace(ctx, tr)
	a := &attacher{o: o, ssh: &SSH{Host: o.Host}, fd: int(o.In.Fd()), filter: keys.New(dk, pk),
		key: o.Host.Name + "/" + o.Session, swallow: map[int]bool{}, tr: tr}

	last := int64(-1)
	// Detach left the alternate screen, and the terminal threw its cells away:
	// the screen must be drawn again, not continued from the offset.
	if o.State.Valid && o.State.Key == a.key && !o.State.Modes.AltScreen {
		last = o.State.Offset
	}
	conn, agent, err := dialAttach(ctx, o.Host, o.Session, o.Interactive)
	if err != nil {
		return AttachResult{}, err
	}
	a.conn, a.agent = conn, agent
	defer a.closeConnections()
	go a.ssh.masterState(ctx, "attach")
	cols, rows, xpx, ypx := a.size()
	att, err := a.handshake(ctx, last, cols, rows, xpx, ypx, false)
	if err != nil {
		return AttachResult{}, err
	}
	DebugLog().Debug("attached", diag.CatKey, diag.CatAttach, "mode", att.Mode, "from", att.From, "live", att.Live)

	original, err := xterm.GetState(a.fd)
	if err != nil {
		return AttachResult{}, fmt.Errorf("save the terminal state: %w", err)
	}
	defer func() { _ = xterm.Restore(a.fd, original) }()
	a.inCh = make(chan []byte, 256)
	a.in, err = startAttachInput(o.In, a.inCh, a.tr)
	if err != nil {
		return AttachResult{}, err
	}
	if _, err := xterm.MakeRaw(a.fd); err != nil {
		a.in.finish()
		return AttachResult{}, fmt.Errorf("cannot put the terminal in raw mode: %w", err)
	}
	if a.agent == nil {
		a.askInput()
	}

	// The paste guard takes the terminal's state that the last attach in
	// this process left only now, when nothing more can fail before the
	// detach gives it back.
	a.guard = newPasteGuard(clipMemoryAt(clipPath()))
	// Remember the title, to put it back on detach. While attached the
	// title is the agent's: the client draws nothing of its own.
	a.write(ctx, []byte("\x1b[22;0t"))
	a.begin(ctx, att, o.State, rows)
	a.startAgent(ctx)

	runErr := a.loop(ctx)
	a.stopAgent()

	// Stop the reader before its files are closed, and before anything else
	// reads the terminal. A read that could not be stopped stays open rather
	// than race with its close.
	a.res.ReaderStuck = a.in.finish()
	// Put the terminal back exactly as we found it: undo the session's modes,
	// never with RIS, which would wipe the scrollback.
	cleanupCtx := context.WithoutCancel(ctx)
	a.write(cleanupCtx, a.tracker.M.ResetSequence())
	a.endGuard(cleanupCtx)
	a.flushPendingNotify(cleanupCtx)
	a.write(cleanupCtx, []byte("\x1b]9;4;0\x1b\\\x1b[23;0t"))

	o.State.Key, o.State.Offset, o.State.Modes, o.State.Valid = a.key, a.cu.Offset, a.tracker.M, true
	if a.res.Ended != nil {
		o.State.Valid = false
	}
	return a.res, runErr
}

// write sends bytes to the terminal: the client's own, and the session's
// title and modes that it sends again. The paste guard reads every byte that
// the terminal gets, and only reads it.
func (a *attacher) write(ctx context.Context, b []byte) { a.writeChecked(ctx, b, false) }

// writeChecked is write for bytes that the caller may have checked: free
// says that c1Free(b) holds, and false says nothing.
func (a *attacher) writeChecked(ctx context.Context, b []byte, free bool) {
	if len(b) > 0 {
		a.guard.observeChecked(ctx, b, free)
		_, _ = a.o.Out.Write(b)
	}
}

// show sends the session's bytes to the terminal, unchanged, then any
// notice that waited for the terminal to be between sequences.
func (a *attacher) show(ctx context.Context, b []byte) {
	a.writeOutput(ctx, b)
}

func (a *attacher) size() (cols, rows, xpx, ypx int) { return termSize(a.o.Out) }

// begin prepares the screen for the bytes that follow.
func (a *attacher) begin(ctx context.Context, att proto.Attached, st *AttachState, rows int) {
	a.cu.History(att.Live)
	if att.Mode == "replay" && st.Valid && st.Key == a.key {
		// The terminal still shows this session up to st.Offset: put its modes back and continue.
		a.tracker = &term.Tracker{M: st.Modes}
		a.cu.Offset = att.From
		a.write(ctx, st.Modes.RestoreSequence())
		return
	}
	// A full redraw. Keep what is on screen in the scrollback, then start clean.
	a.tracker = term.NewTracker()
	a.outStream.reset()
	a.cu.Offset = att.From
	// For "snapshot", a TSnapshot frame redraws the screen as of From, and the
	// output from From follows; bytes before Live are history and are sanitised.
	if a.o.Settings.Attach.Scrollback == "clear" {
		a.write(ctx, []byte("\x1b[H\x1b[2J\x1b[3J"))
	} else {
		a.write(ctx, []byte(fmt.Sprintf("\x1b[%d;1H%s\x1b[H", rows, strings.Repeat("\n", rows))))
	}
	a.flushPendingNotify(ctx)
}

// output shows bytes from offset off, skipping any the terminal already has.
func (a *attacher) output(ctx context.Context, off int64, data []byte) {
	show, fresh := a.cu.Feed(off, data)
	if fresh == nil {
		return // the terminal already shows it
	}
	a.writeOutput(ctx, show)
	a.tracker.Feed(fresh)
}

func (a *attacher) loop(ctx context.Context) error {
	winch, stopWinch := watchResize(a.o.Out)
	defer stopWinch()
	heart := time.NewTicker(a.tr.pingEvery())
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
	var agentHeld []byte
	for {
		select {
		case now := <-wake.C:
			gap := now.Sub(lastTick)
			lastTick = now
			if gap > wakeGap {
				a.dropped = "the laptop slept"
				a.stopAgent()
				a.conn.kill()
			}
		case <-watch.C:
			if err := a.watchAgentGrant(ctx); err != nil {
				return err
			}
			a.watchOthers(ctx, others)
		case list := <-others:
			a.notifyOthers(ctx, list)
		case f := <-a.conn.Stream():
			if done, err := a.frame(ctx, f); done || err != nil {
				return err
			}
		case ready := <-a.agentReady:
			if stop, err := a.onAgentReady(ctx, ready, &agentHeld, &detachTimer, &flushTimer); stop || err != nil {
				return err
			}
		case <-a.agentDone():
			if err := a.onAgentLost(); err != nil {
				return err
			}
		case b, ok := <-a.inCh:
			a.inputPending = false
			if !ok {
				a.res.Detached = true
				return nil
			}
			if a.agentStarting {
				agentHeld = b
				continue
			}
			if a.onKeys(ctx, b, ok, &detachTimer, &flushTimer) {
				return nil
			}
			a.askInput()
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
			agentHeld = nil
			a.stopAgent()
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
	a.tr.took()
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
		a.dropped = "no answer from the host"
		a.stopAgent()
		a.conn.kill()
	}
}

// frame handles one frame from the host. done is true when the attach is over.
func (a *attacher) frame(ctx context.Context, f proto.Frame) (done bool, err error) {
	switch f.Type {
	case proto.TOutput:
		off, data, err := proto.DecodeOutput(f.Payload)
		if err == nil {
			a.output(ctx, off, data)
			if a.tr != nil {
				a.tr.drawn(off, len(data))
			}
		}
	case proto.TSnapshot:
		// A redraw of the current screen, made by the host's terminal emulator.
		// It contains no queries, so it goes straight to the terminal.
		a.show(ctx, f.Payload)
		a.tracker.Feed(f.Payload)
	case proto.TEnded:
		var e proto.Ended
		_ = json.Unmarshal(f.Payload, &e)
		// The signal's name is drawn, in the notice at the end of an attach.
		e.Signal = CleanLine(e.Signal)
		a.res.Ended = &e
		return true, nil
	case proto.TError:
		var pe proto.Error
		_ = json.Unmarshal(f.Payload, &pe)
		if pe.Code == "session_gone" {
			a.res.Ended = &proto.Ended{ExitCode: -1}
			return true, nil
		}
		return true, hostError(&pe)
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
