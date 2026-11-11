package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/ring"
)

// These tests check the caps on a terminal's size: a client may ask for up
// to 65535 columns and rows. The host gives the PTY and the shadow terminal
// at most maxTermCols by maxTermRows, and its screen model at most
// screenMaxCols by screenMaxRows.

func TestClampColsAndRows(t *testing.T) {
	for _, c := range []struct {
		in         int
		cols, rows uint16
	}{
		{-1, 1, 1}, {0, 1, 1}, {1, 1, 1}, {80, 80, 80}, {2048, 2048, 2048}, {2049, 2049, 2048},
		{4096, 4096, 2048}, {4097, 4096, 2048}, {65535, 4096, 2048}, {math.MaxInt, 4096, 2048},
	} {
		if got := clampCols(c.in); got != c.cols {
			t.Errorf("clampCols(%d) = %d, want %d", c.in, got, c.cols)
		}
		if got := clampRows(c.in); got != c.rows {
			t.Errorf("clampRows(%d) = %d, want %d", c.in, got, c.rows)
		}
	}
}

// sizeHolder returns a holder whose agent's terminal is a new PTY, with a
// screen model, v as its shadow terminal and c as its leader. It logs as a
// holder does, to logs. It also returns the PTY's other end, where the
// agent reads its size.
func sizeHolder(t *testing.T, v *VT, logs *lineLog) (h *Holder, c *hclient, tty *os.File) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })
	c = &hclient{}
	h = &Holder{id: "termsize", log: logs.logger(), ptmx: ptmx, fd: int(ptmx.Fd()), m: &Manifest{},
		leader: c, vt: v, scr: newScreenModel(80, 24)}
	return h, c, tty
}

// clampLines counts the lines about a clamped size.
func clampLines(l *lineLog) int {
	n := 0
	for _, line := range l.get() {
		if strings.Contains(line, `msg="resize clamped"`) {
			n++
		}
	}
	return n
}

// screenSize is the size of the screen model's emulator that shows.
func screenSize(m *screenModel) (cols, rows int) {
	e := m.current()
	return e.Width(), e.Height()
}

// A resize to 65535 by 65535 sets the PTY, the shadow terminal and the
// manifest to 4096 by 2048, and the screen model to 1000 by 500. The
// holder's log says so. Pixel sizes keep their own cap of 65535.
func TestHolderResizeCapsTheSize(t *testing.T) {
	path, input := fakeVT(t)
	v, err := StartVT(VTOptions{Path: path, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var logs lineLog
	h, c, tty := sizeHolder(t, v, &logs)

	h.resize(c, proto.Resize{Cols: 65535, Rows: 65535, XPx: 70000, YPx: 800}, false)
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	if ws, err := pty.GetsizeFull(tty); err != nil || ws.Cols != 4096 || ws.Rows != 2048 || ws.X != 65535 || ws.Y != 800 {
		t.Errorf("the PTY's size = %+v, %v; want 4096 by 2048, and 65535 by 800 pixels", ws, err)
	}
	if h.m.Term.Cols != 4096 || h.m.Term.Rows != 2048 {
		t.Errorf("the manifest's size = %d by %d, want 4096 by 2048", h.m.Term.Cols, h.m.Term.Rows)
	}
	if cols, rows := screenSize(h.scr); cols != 1000 || rows != 500 {
		t.Errorf("the screen model is %d by %d, want 1000 by 500", cols, rows)
	}
	// The first frame is the start size, 80 by 24. The resize is 4096
	// (0x1000) by 2048 (0x800), with 65535 by 800 (0x320) pixels.
	want := `R"\x00\x10\x00\b\xff\xff \x03"`
	if got := childFrames(t, input, 4); !slices.Contains(got, want) {
		t.Errorf("the shadow terminal got %q, want a frame %s", got, want)
	}
	if !logs.has("level=INFO", `msg="resize clamped"`, "asked_cols=65535", "asked_rows=65535", "cols=4096", "rows=2048") {
		t.Errorf("the holder's log has no line for the clamp: %q", logs.get())
	}

	// A size within the caps is set as it is, with no line about a clamp.
	h.resize(c, proto.Resize{Cols: 4096, Rows: 30}, false)
	if ws, err := pty.GetsizeFull(tty); err != nil || ws.Cols != 4096 || ws.Rows != 30 {
		t.Errorf("the PTY's size = %+v, %v; want 4096 by 30", ws, err)
	}
	if cols, rows := screenSize(h.scr); cols != 1000 || rows != 30 {
		t.Errorf("the screen model is %d by %d, want 1000 by 30", cols, rows)
	}
	if n := clampLines(&logs); n != 1 {
		t.Errorf("%d lines about a clamp, want 1: %q", n, logs.get())
	}

	// Another client's size goes nowhere, and is not logged.
	h.resize(&hclient{}, proto.Resize{Cols: 65535, Rows: 65535}, false)
	if ws, err := pty.GetsizeFull(tty); err != nil || ws.Cols != 4096 || ws.Rows != 30 {
		t.Errorf("after another client's resize, the PTY's size = %+v, %v; want 4096 by 30", ws, err)
	}
	if n := clampLines(&logs); n != 1 {
		t.Errorf("another client's resize was logged: %q", logs.get())
	}
}

// The screen model keeps its own cap of 1000 by 500, on both screens, at its
// start and on a resize.
func TestScreenModelCap(t *testing.T) {
	m := newScreenModel(4096, 4096)
	if cols, rows := screenSize(m); cols != 1000 || rows != 500 {
		t.Errorf("a new model of 4096 by 4096 is %d by %d, want 1000 by 500", cols, rows)
	}
	m = newScreenModel(80, 24)
	m.resize(4096, 4096)
	if cols, rows := screenSize(m); cols != 1000 || rows != 500 {
		t.Errorf("after a resize to 4096 by 4096, the model is %d by %d, want 1000 by 500", cols, rows)
	}
	m.feed([]byte("\x1b[?1049hALT"))
	if !m.altScreen() {
		t.Fatal("the model did not switch to the alternate screen")
	}
	if cols, rows := screenSize(m); cols != 1000 || rows != 500 {
		t.Errorf("the alternate screen is %d by %d, want 1000 by 500", cols, rows)
	}
	m.resize(1200, 300)
	if cols, rows := screenSize(m); cols != 1000 || rows != 300 {
		t.Errorf("after a resize to 1200 by 300, the alternate screen is %d by %d, want 1000 by 300", cols, rows)
	}
	if m.off {
		t.Error("the model turned itself off")
	}
}

// VT caps the sizes that it passes to its child: on the command line at the
// start, in a resize frame and in a snapshot frame. Pixel sizes keep
// clamp16. It also keeps the capped size for the next child.
func TestVTSendsCappedSizes(t *testing.T) {
	path, input := fakeVT(t)
	var logs lineLog
	v, err := StartVT(VTOptions{Path: path, Cols: 65535, Rows: 70000, Log: logs.logger()})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	v.Resize(65535, 65535, 70000, 800)
	if _, _, err := v.Snapshot(65535, 65535, 10, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	got := childFrames(t, input, 4)
	if !logs.has("fake shadow terminal --cols 4096 --rows 2048") {
		t.Errorf("the child's command line was not capped: %q", logs.get())
	}
	for _, want := range []string{
		`R"\x00\x10\x00\b\x00\x00\x00\x00"`, // start: 4096 by 2048
		`R"\x00\x10\x00\b\xff\xff \x03"`,    // resize: 4096 by 2048, 65535 by 800 pixels
		`S"\x00\x10\x00\b\n\x00\x00\x00"`,   // snapshot: 4096 by 2048, 10 lines
	} {
		if !slices.Contains(got, want) {
			t.Errorf("the shadow terminal got %q, want a frame %s", got, want)
		}
	}
	v.mu.Lock()
	cols, rows := v.cols, v.rows
	v.mu.Unlock()
	if cols != 4096 || rows != 2048 {
		t.Errorf("the VT keeps %d by %d, want 4096 by 2048", cols, rows)
	}
}

// The real shadow terminal ends up at the capped size too.
func TestHolderResizeCapsTheShadowTerminal(t *testing.T) {
	v := vtStartTest(t, VTOptions{Cols: 80, Rows: 24})
	var logs lineLog
	h, c, _ := sizeHolder(t, v, &logs)
	h.resize(c, proto.Resize{Cols: 65535, Rows: 65535}, false)
	if d := vtDumpOf(t, v, 0); d.Cols != 4096 || d.Rows != 2048 {
		t.Errorf("the shadow terminal is %d by %d, want 4096 by 2048", d.Cols, d.Rows)
	}
}

// The agent starts at the last size a client set, capped, with 80 by 24 for
// a number that it did not set. The manifest keeps the size that the PTY
// gets, so the session's row and peek report it, unless the client set no
// size at all. A clamped size writes a line to the holder's log.
func TestHolderStartSize(t *testing.T) {
	for _, c := range []struct {
		term       TermInfo
		cols, rows int // the PTY's size
		saved      TermInfo
		clamped    string // the clamp line's sizes, or "" for none
	}{
		{TermInfo{Cols: 65535, Rows: 50}, 4096, 50, TermInfo{Cols: 4096, Rows: 50}, "asked_cols=65535 asked_rows=50 cols=4096 rows=50"},
		{TermInfo{Cols: 65535}, 4096, 24, TermInfo{Cols: 4096, Rows: 24}, "asked_cols=65535 asked_rows=24 cols=4096 rows=24"},
		{TermInfo{Rows: 65535}, 80, 2048, TermInfo{Cols: 80, Rows: 2048}, "asked_cols=80 asked_rows=65535 cols=80 rows=2048"},
		{TermInfo{Cols: 120, Rows: 40}, 120, 40, TermInfo{Cols: 120, Rows: 40}, ""},
		{TermInfo{}, 80, 24, TermInfo{}, ""},
	} {
		var logs lineLog
		h := &Holder{log: logs.logger(), m: &Manifest{Term: c.term}, ring: ring.New(1 << 10)}
		if cols, rows := h.startSize(); cols != c.cols || rows != c.rows {
			t.Errorf("%+v: start size = %d by %d, want %d by %d", c.term, cols, rows, c.cols, c.rows)
		}
		if h.m.Term != c.saved {
			t.Errorf("%+v: the manifest says %+v, want %+v", c.term, h.m.Term, c.saved)
		}
		if row := infoFrom(h.m, true); row.Cols != c.saved.Cols || row.Rows != c.saved.Rows {
			t.Errorf("%+v: the session's row says %d by %d, want %d by %d", c.term, row.Cols, row.Rows, c.saved.Cols, c.saved.Rows)
		}
		if cols, rows := peekSize(t, h); cols != c.saved.Cols || rows != c.saved.Rows {
			t.Errorf("%+v: peek says %d by %d, want %d by %d", c.term, cols, rows, c.saved.Cols, c.saved.Rows)
		}
		switch {
		case c.clamped == "" && clampLines(&logs) != 0:
			t.Errorf("%+v: a size within the cap was logged: %q", c.term, logs.get())
		case c.clamped != "" && !logs.has("level=INFO", `msg="resize clamped" `+c.clamped):
			t.Errorf("%+v: the holder's log has no line %q: %q", c.term, c.clamped, logs.get())
		}
	}
}

// peekSize asks the holder for a peek, as a client does, and returns the
// size in the answer.
func peekSize(t *testing.T, h *Holder) (cols, rows int) {
	t.Helper()
	var out bytes.Buffer
	h.request(&hclient{w: proto.NewWriter(&out)}, proto.Request{ID: 1, Op: "peek"})
	f, err := proto.Read(bufio.NewReader(&out))
	if err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	var size struct{ Cols, Rows int }
	if err := json.Unmarshal(f.Payload, &resp); err != nil || !resp.OK {
		t.Fatalf("peek answered %s, %v", f.Payload, err)
	}
	if err := json.Unmarshal(resp.Result, &size); err != nil {
		t.Fatal(err)
	}
	return size.Cols, size.Rows
}

// spawn caps the size on the child's command line, even when its caller did
// not cap it. Its callers pass a capped size, so this cap is a guard.
func TestSpawnCapsTheSize(t *testing.T) {
	path, _ := fakeVT(t)
	var logs lineLog
	v := &VT{opt: VTOptions{Path: path, Log: logs.logger()}, log: logs.logger(), done: make(chan struct{})}
	c, err := v.spawn(65535, 70000)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return logs.has("fake shadow terminal") })
	c.kill()
	<-c.gone
	if !logs.has(`line="fake shadow terminal --cols 4096 --rows 2048"`) {
		t.Errorf("the child's command line was not capped: %q", logs.get())
	}
}
