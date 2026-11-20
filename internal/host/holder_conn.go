package host

import (
	"bufio"
	"encoding/json"
	"net"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// acceptLoop serves each connection to ln, the session socket or, when
// hookSock is true, the hook socket.
func (h *Holder) acceptLoop(ln net.Listener, hookSock bool) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go h.serve(conn, hookSock)
	}
}

func (h *Holder) serve(conn net.Conn, hookSock bool) {
	c := &hclient{conn: conn, w: proto.NewWriter(conn), out: make(chan outFrame, clientQueue), done: make(chan struct{})}
	defer func() {
		h.drop(c)
		c.close()
	}()
	r := bufio.NewReaderSize(conn, 64<<10)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.THello {
		return
	}
	var hello proto.Hello
	if json.Unmarshal(f.Payload, &hello) != nil || hello.Check(version.Protocol) != nil {
		_ = c.w.WriteJSON(proto.TError, proto.Error{Code: "protocol", Message: "incompatible client"})
		return
	}
	var refused *proto.Error
	if c.access, refused = h.accessOf(conn, hookSock, hello.Role); refused != nil {
		h.log.Warn("refused a connection", "role", hello.Role, "hook_socket", hookSock)
		_ = c.w.WriteJSON(proto.TError, refused)
		return
	}
	if err := c.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "holder"}); err != nil {
		return
	}
	h.dispatch(c, r)
}

// dispatch serves the frames of one connection after its hello, as its
// access allows, until the connection ends or a refusal closes it.
func (h *Holder) dispatch(c *hclient, r *bufio.Reader) {
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		// The role and peer check (holder_peer.go). A connection with full
		// access takes the path below, frame for frame as before.
		if c.access != accessFull {
			if !h.serveLimited(c, f) {
				return
			}
			continue
		}
		switch f.Type {
		case proto.TAttach:
			var a proto.Attach
			if json.Unmarshal(f.Payload, &a) == nil {
				h.attach(c, a)
			}
		case proto.TTrace:
			c.bridgeRead(f.Payload)
		case proto.TInput:
			h.input(c, f.Payload)
		case proto.TResize:
			var rz proto.Resize
			if json.Unmarshal(f.Payload, &rz) == nil {
				h.resize(c, rz, false)
			}
		case proto.TDetach:
			h.drop(c)
		case proto.TPing:
			_ = c.w.Write(proto.TPong, f.Payload)
		case proto.TRequest:
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) == nil {
				h.request(c, req)
			}
		default:
			// Other frames are not for a holder: ignore them.
		}
	}
}

func (h *Holder) attach(c *hclient, a proto.Attach) {
	h.traceAttach(c, a.Trace, a.Categories)
	snap, snapAt := h.snapshotFor(a)
	h.mu.Lock()
	mode, from := "full", h.ring.Start()
	switch {
	case a.LastOffset >= 0 && h.ring.Has(a.LastOffset):
		mode, from = "replay", a.LastOffset
	case snapAt >= 0 && h.ring.Has(snapAt):
		mode, from = "snapshot", snapAt
	}
	data, _ := h.ring.ReadFrom(from)
	live := h.ring.End()
	c.attached = true
	h.clients[c] = struct{}{}
	h.leader = c
	exited := h.exited
	ended := h.ended
	h.m.LastAttachedAt = time.Now().UTC()
	h.noteAttach(a)
	h.dirty = true
	h.mu.Unlock()

	h.log.Debug("attach", diag.CatKey, diag.CatAttach, "mode", mode, "from", from, "live", live, "cols", a.Cols, "rows", a.Rows)
	if h.vt != nil {
		h.vt.AnswerQueries(false) // a client's terminal answers from now on
	}
	if err := c.w.WriteJSON(proto.TAttached, proto.Attached{Session: h.id, Mode: mode, From: from, Live: live, Exited: exited}); err != nil {
		c.close()
		return
	}
	if !c.catchUp(mode, snap, from, data) {
		return
	}
	go c.writeLoop()
	if exited {
		b, _ := json.Marshal(ended)
		c.out <- outFrame{proto.TEnded, b}
		return
	}
	h.resize(c, proto.Resize{Cols: a.Cols, Rows: a.Rows, XPx: a.XPx, YPx: a.YPx}, true)
}

// snapshotFor takes the snapshot an attaching client gets when it has none
// of this session's output: from the shadow terminal, before the attach
// takes the lock, so output keeps flowing. snapAt is the stream offset the
// snapshot reaches, or -1 when there is no snapshot.
func (h *Holder) snapshotFor(a proto.Attach) (snap []byte, snapAt int64) {
	snapAt = -1
	h.mu.Lock()
	canReplay := a.LastOffset >= 0 && h.ring.Has(a.LastOffset)
	vt := h.vt
	h.mu.Unlock()
	if !canReplay && vt != nil {
		if s, upTo, err := vt.Snapshot(a.Cols, a.Rows, snapshotLines, 3*time.Second); err == nil {
			snap, snapAt = s, upTo
		} else {
			h.log.Warn("snapshot failed, replaying the ring", "err", err)
		}
	}
	return snap, snapAt
}

// catchUp writes what an attaching client missed: the snapshot in snapshot
// mode, then the output from offset from on, in frames of 256 KiB at most.
// When a write fails, it closes c and returns false.
func (c *hclient) catchUp(mode string, snap []byte, from int64, data []byte) bool {
	if mode == "snapshot" {
		for len(snap) > 0 {
			n := min(len(snap), 256<<10)
			if err := c.w.Write(proto.TSnapshot, snap[:n]); err != nil {
				c.close()
				return false
			}
			snap = snap[n:]
		}
	}
	for off := from; len(data) > 0; {
		n := len(data)
		if n > 256<<10 {
			n = 256 << 10
		}
		if err := c.w.Write(proto.TOutput, proto.EncodeOutput(off, data[:n])); err != nil {
			c.close()
			return false
		}
		off += int64(n)
		data = data[n:]
	}
	return true
}

// writeLoop writes the frames queued for c, in order, until c is closed or a
// write fails.
func (c *hclient) writeLoop() {
	for {
		select {
		case f := <-c.out:
			if err := c.w.Write(f.t, f.p); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (h *Holder) input(c *hclient, p []byte) {
	lt := h.lockTimed()
	if h.exited || !c.attached {
		h.mu.Unlock()
		return
	}
	h.leader = c
	h.needInput = false
	h.notifyMsg = ""
	d := h.al.d
	h.mu.Unlock()
	noteTyping(d, p)
	if _, err := h.ptmx.Write(p); err != nil {
		h.log.Warn("pty write failed", "err", err)
	}
	lt.key(c, len(p))
}

func (h *Holder) resize(c *hclient, rz proto.Resize, force bool) {
	if rz.Cols <= 0 || rz.Rows <= 0 {
		return
	}
	asked := rz
	rz.Cols, rz.Rows = capTerm(rz.Cols, rz.Rows)
	h.mu.Lock()
	leader := h.leader == c
	exited := h.exited
	if leader {
		h.m.Term.Cols, h.m.Term.Rows = rz.Cols, rz.Rows
		h.dirty = true
	}
	h.mu.Unlock()
	if !leader || exited {
		return
	}
	h.noteClamp(asked, rz)
	h.log.Debug("resize", diag.CatKey, diag.CatAttach, "cols", rz.Cols, "rows", rz.Rows, "force", force)
	_ = pty.Setsize(h.ptmx, &pty.Winsize{Cols: clampCols(rz.Cols), Rows: clampRows(rz.Rows), X: clamp16(rz.XPx), Y: clamp16(rz.YPx)})
	h.mu.Lock()
	h.scr.resize(rz.Cols, rz.Rows) // the screen model has a smaller cap of its own
	vt := h.vt
	h.mu.Unlock()
	if vt != nil {
		vt.Resize(rz.Cols, rz.Rows, rz.XPx, rz.YPx)
	}
	if force {
		// Make the agent repaint even when the size did not change.
		if pgrp, err := unix.IoctlGetInt(h.fd, unix.TIOCGPGRP); err == nil && pgrp > 0 {
			_ = unix.Kill(-pgrp, unix.SIGWINCH)
		}
	}
}

// startSize is the size of the agent's terminal when it starts: the last
// size that a client set, or 80 by 24 when there is none, within
// maxTermCols by maxTermRows. The manifest keeps the size that it sets, so
// the session's row and peek report it.
func (h *Holder) startSize() (cols, rows int) {
	h.mu.Lock()
	term := h.m.Term
	asked := proto.Resize{Cols: term.Cols, Rows: term.Rows}
	if asked.Cols <= 0 {
		asked.Cols = 80
	}
	if asked.Rows <= 0 {
		asked.Rows = 24
	}
	set := asked
	set.Cols, set.Rows = capTerm(asked.Cols, asked.Rows)
	// The manifest keeps "no size" only when a client gave neither number.
	if term.Cols > 0 || term.Rows > 0 {
		h.m.Term.Cols, h.m.Term.Rows = set.Cols, set.Rows
	}
	h.mu.Unlock()
	h.noteClamp(asked, set)
	return set.Cols, set.Rows
}

// noteClamp writes a line to the holder's log when the cap changed a
// client's terminal size: the size asked for, and the size set.
func (h *Holder) noteClamp(asked, set proto.Resize) {
	if asked.Cols != set.Cols || asked.Rows != set.Rows {
		h.log.Info("resize clamped", "asked_cols", asked.Cols, "asked_rows", asked.Rows, "cols", set.Cols, "rows", set.Rows)
	}
}

func (h *Holder) drop(c *hclient) {
	h.traceOff(c)
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	c.attached = false
	if h.leader == c {
		h.leader = nil
	}
	if h.vt != nil && !h.anyAttached() {
		h.vt.AnswerQueries(true)
	}
}

// anyAttached reports whether a client is attached. The caller holds h.mu.
func (h *Holder) anyAttached() bool {
	for c := range h.clients {
		if c.attached {
			return true
		}
	}
	return false
}
