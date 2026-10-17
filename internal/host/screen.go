package host

import (
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/vt"

	"github.com/iamlakshya/simstim/internal/agents"
)

// screenModel keeps a rendered copy of the session's screen, only to read its
// text for status rules. If an emulator ever panics, the model turns itself
// off: status falls back to the other signals, and the agent is never touched.
//
// It keeps one emulator for each screen. e gets the output for the main
// screen. While the program draws on the alternate screen, alt gets that
// output instead, on its own main screen. The router (screen_route.go) splits
// the output between them, and keeps each switch from both.
type screenModel struct {
	e     *vt.Emulator // the main screen
	alt   *vt.Emulator // the alternate screen, or nil while the main screen shows
	spare *vt.Emulator // the last alternate screen, kept to use again
	rt    router
	title string
	dirty bool
	off   bool
}

// screenScrollback is how many lines each emulator keeps after they scroll
// off its screen. Nothing reads them: the status rules read only the visible
// screen. One line is the least that vt v0.1.0 keeps, because it reads a size
// of 0 as its default of 10,000 lines. Those lines held 60 to 70 MiB in a
// holder whose agent printed a long transcript, and that memory counted
// against the session's memory cap.
//
// vt v0.1.0 also gives its alternate screen a scrollback of 10,000 lines, and
// SetScrollbackSize does not reach it. A full-screen program that scrolled
// that screen filled it, up to about 62 MiB. So no emulator here ever
// switches to its own alternate screen. The model draws that screen on the
// main screen of a second emulator, which keeps one line too.
const screenScrollback = 1

// Sequences that the model writes to its own emulators. No terminal draws
// these emulators, so none of these bytes reach the user's terminal.
var (
	seqRIS     = []byte("\x1bc")       // a full reset
	seqDECSC   = []byte("\x1b7")       // save the cursor
	seqCAN     = []byte{0x18}          // drop the sequence that vt reads
	seqMainScr = []byte("\x1b[?1047l") // show the main screen
)

func newScreenModel(cols, rows int) *screenModel {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	m := &screenModel{e: newScreenEmulator(cols, rows)}
	m.off = m.e == nil
	m.rt.shared, m.rt.mainShared = freshShared, freshShared
	return m
}

// newScreenEmulator returns an emulator that keeps screenScrollback lines. It
// returns nil if it cannot drop the emulator's replies to queries.
func newScreenEmulator(cols, rows int) *vt.Emulator {
	e := vt.NewEmulator(cols, rows)
	e.SetScrollbackSize(screenScrollback)
	// The emulator writes its replies into a pipe that nothing reads. With
	// the pipe closed, each reply fails at once. So the emulator never waits
	// for a reader, and no goroutine keeps a dropped emulator alive.
	pw, ok := e.InputPipe().(io.Closer)
	if !ok {
		return nil
	}
	_ = pw.Close()
	return e
}

// feed passes the program's output to the emulators.
func (m *screenModel) feed(p []byte) {
	if m == nil || m.off {
		return
	}
	defer func() {
		if recover() != nil {
			m.off = true
		}
	}()
	m.route(p)
	m.dirty = true
}

// current returns the emulator of the screen that shows.
func (m *screenModel) current() *vt.Emulator {
	if m.alt != nil {
		return m.alt
	}
	return m.e
}

// pass writes p to the emulator of the screen that shows.
func (m *screenModel) pass(p []byte) {
	e := m.current()
	_, _ = e.Write(p)
	if e.IsAltScreen() {
		// The emulator took a sequence as a switch that the router did not.
		// Its own alternate screen keeps 10,000 lines, so leave it at once.
		_, _ = e.Write(seqMainScr)
	}
}

// showAlt shows the alternate screen, or the main screen again. A switch to
// the screen that already shows changes nothing. mode is the DEC private
// mode that switches: 47, 1047 or 1049.
//
// The alternate screen starts blank. On the way back, the main emulator shows
// the main screen as it was, because it saw none of the alternate screen's
// output.
// After 1049, its cursor is where it was at the switch, which is where 1049
// restores it in a terminal. After 47 or 1047, a terminal keeps the cursor
// where the alternate screen left it, so the main emulator's cursor moves
// there.
func (m *screenModel) showAlt(on bool, mode int) {
	switch {
	case on == (m.alt != nil):
		// That screen shows already.
	case on:
		m.alt = m.blankAlt()
		if m.alt == nil {
			m.off = true
			return
		}
		if m.rt.shared != freshShared {
			m.passSharedModes()
		}
		m.rt.mainShared = m.rt.shared
	default:
		pos := m.alt.CursorPosition()
		m.spare, m.alt = m.alt, nil
		if m.rt.shared != m.rt.mainShared {
			m.passSharedModes()
		}
		if mode != 1049 {
			b := append([]byte("\x1b["), strconv.Itoa(pos.Y+1)...)
			b = append(append(append(b, ';'), strconv.Itoa(pos.X+1)...), 'H')
			m.pass(b)
		}
	}
}

// blankAlt returns a blank emulator of the main screen's size, for the
// alternate screen. It uses the spare one again if there is one, so that a
// program that switches often does not make a new emulator each time. A new
// one costs about 4.8 MB, most of it a buffer that vt's parser makes.
func (m *screenModel) blankAlt() *vt.Emulator {
	w, h := m.e.Width(), m.e.Height()
	e := m.spare
	m.spare = nil
	if e == nil {
		return newScreenEmulator(w, h)
	}
	// RIS resets this emulator. It goes only to this emulator, which no
	// terminal draws, never to the user's terminal (AGENTS.md, rule 2).
	_, _ = e.Write(seqRIS)
	e.ClearScrollback()
	if e.Width() != w || e.Height() != h {
		e.Resize(w, h)
	}
	return e
}

// reset follows RIS: the terminal resets, and shows its main screen.
func (m *screenModel) reset() {
	if m.alt != nil {
		m.spare, m.alt = m.alt, nil
	}
	m.rt.shared = freshShared
	m.resetTabs()
	m.pass(seqRIS)
}

func (m *screenModel) resize(cols, rows int) {
	if m == nil || m.off || cols <= 0 || rows <= 0 {
		return
	}
	defer func() {
		if recover() != nil {
			m.off = true
		}
	}()
	m.e.Resize(cols, rows)
	if m.alt != nil {
		m.alt.Resize(cols, rows)
	}
	// vt sets its default tab stops again on a resize.
	m.resetTabs()
	m.dirty = true
}

// altScreen is true while the program draws on the alternate screen.
func (m *screenModel) altScreen() bool {
	return m != nil && !m.off && m.alt != nil
}

// screen returns the visible text, one string per row.
func (m *screenModel) screen() (s agents.Screen) {
	if m == nil || m.off {
		return s
	}
	defer func() {
		if recover() != nil {
			m.off = true
		}
	}()
	s.Title = m.title
	s.Lines = screenLines(m.current())
	return s
}

// screenLines returns the text that an emulator shows, one string per row.
func screenLines(e *vt.Emulator) []string {
	w, h := e.Width(), e.Height()
	lines := make([]string, 0, h)
	var b strings.Builder
	for y := range h {
		b.Reset()
		for x := range w {
			if c := e.CellAt(x, y); c != nil && c.Content != "" {
				b.WriteString(c.Content)
			} else if c == nil || c.Width != 0 {
				b.WriteByte(' ')
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}
