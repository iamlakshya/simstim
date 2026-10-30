package tui

import (
	"context"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// peekBytes is how much of a session's newest output the preview reads when
// the host has no snapshot of its screen.
const peekBytes = 64 << 10

// previewState is the preview of the selected session.
type previewState struct {
	key      sessKey
	seq      int
	loading  bool
	inflight bool
	shot     *screenShot
	err      string
}

type peekMsg struct {
	key  sessKey
	shot *screenShot
	err  error
}

// previewModal shows the selected session's last screen over the list
// (docs/contributing/ux-spec.md 2.6). It opens on Space and is never a permanent pane.
type previewModal struct{}

func (m *Model) previewOpen() bool {
	_, ok := m.modal.(*previewModal)
	return ok
}

// openPreview shows the preview and loads it at once.
func (m *Model) openPreview() tea.Cmd {
	if m.selected() == nil {
		return nil
	}
	m.modal = &previewModal{}
	m.preview = previewState{key: m.sel, seq: m.preview.seq + 1, loading: true}
	return m.peek()
}

func (p *previewModal) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "space", "q":
		m.modal = nil
	case "enter":
		if s := m.selected(); s != nil && ended(*s) {
			return nil // the agent's last screen is what Enter opens
		}
		m.modal = nil
		return m.attachSelected()
	case "up", "k":
		m.moveSel(-1)
	case "down", "j":
		m.moveSel(1)
	}
	return nil
}

func (p *previewModal) view(m *Model) string {
	hc, s := m.selectedPair()
	if s == nil {
		return ""
	}
	w := m.dialogWidth()
	cw := w - 4
	_, label := m.look(*s)
	hints := []hint{{"enter", "attach"}, {"↑↓", "next"}, {"esc", "close"}}
	mem := fmtMemory(s.MemoryCurrent, s.MemoryMax)
	if ended(*s) {
		// The last screen of an agent that has ended: nothing to attach to,
		// and the memory is no longer the agent's.
		hints, mem = hints[1:], ""
	}
	var facts []string
	for _, f := range []string{s.Agent, truncPath(homePath(hc, s.Cwd), max(cw/3, 16)), mem} {
		if f != "" {
			facts = append(facts, f)
		}
	}
	preview := m.previewLines(cw, max(m.height-9, 3))
	body := make([]string, 0, 2+len(preview))
	body = append(body, sFaint.Render(trunc(strings.Join(facts, dot), cw)), "")
	body = append(body, preview...)
	return sheet(w, "Peek · "+s.Name, label.st.Render(label.s), hints, body, toneNormal)
}

// schedulePreview loads the preview of the selected session after a short
// pause, so moving quickly through the list does not flood the host.
func (m *Model) schedulePreview() tea.Cmd {
	if !m.previewOpen() {
		return nil
	}
	m.preview.seq++
	if m.sel == (sessKey{}) {
		m.preview = previewState{seq: m.preview.seq}
		return nil
	}
	if m.preview.key != m.sel {
		m.preview = previewState{key: m.sel, seq: m.preview.seq, loading: true}
	}
	return m.after(previewDelay, previewTickMsg{seq: m.preview.seq})
}

// peek asks the host for the selected session's screen.
func (m *Model) peek() tea.Cmd {
	if !m.previewOpen() || m.preview.inflight || m.preview.key == (sessKey{}) {
		return nil
	}
	h := m.hostByName(m.preview.key.host)
	if h == nil || !h.online() {
		return nil
	}
	m.preview.inflight = true
	key, c, ctx := m.preview.key, h.conn, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		data, cols, rows, err := c.Peek(cctx, key.id, peekBytes)
		if err != nil {
			return peekMsg{key: key, err: err}
		}
		return peekMsg{key: key, shot: renderScreen(data, cols, rows)}
	}
}

func (m *Model) onPeek(msg peekMsg) {
	if msg.key != m.preview.key {
		return // an answer for a session no longer selected
	}
	m.preview.inflight, m.preview.loading = false, false
	if msg.err != nil {
		e, _ := errText(msg.err)
		m.preview.err = e
		return
	}
	m.preview.shot, m.preview.err = msg.shot, ""
}

// screenShot is a session's screen as the terminal would show it.
type screenShot struct {
	lines  []uv.Line
	bottom int // the last row worth showing: text or the cursor
	blank  bool
}

// renderScreen runs bytes through a terminal emulator of the session's size.
// The bytes are a snapshot of the screen and the output after it, or, from a
// host without one, the tail of the session's output, whose first few bytes
// may be the end of a cut sequence; the emulator shrugs those off.
func renderScreen(data []byte, cols, rows int) (shot *screenShot) {
	// A preview is never worth a crash: odd bytes give an empty screen.
	defer func() {
		if recover() != nil {
			shot = &screenShot{blank: true}
		}
	}()
	if cols <= 0 || cols > 1000 {
		cols = 80
	}
	if rows <= 0 || rows > 500 {
		rows = 24
	}
	emu := vt.NewEmulator(cols, rows)
	// The emulator answers terminal queries through a pipe. Drain it, so a
	// query in the output cannot block the write.
	done := make(chan struct{})
	pw, _ := emu.InputPipe().(*io.PipeWriter)
	if pw != nil {
		go func() {
			_, _ = io.Copy(io.Discard, emu)
			close(done)
		}()
	} else {
		data = stripQueries(data)
		close(done)
	}
	_, _ = emu.Write(data)
	if pw != nil {
		_ = pw.Close()
	}
	<-done

	shot = &screenShot{blank: true}
	for y := range rows {
		line := make(uv.Line, cols)
		for x := range cols {
			if c := emu.CellAt(x, y); c != nil {
				line[x] = *c
			} else {
				line[x] = uv.EmptyCell
			}
		}
		shot.lines = append(shot.lines, line)
		if strings.TrimSpace(line.String()) != "" {
			shot.bottom, shot.blank = y, false
		}
	}
	if pos := emu.CursorPosition(); !emu.CursorHidden() && pos.Y > shot.bottom && pos.Y < rows {
		shot.bottom = pos.Y
	}
	return shot
}

// stripQueries is the fallback when the emulator's reply pipe cannot be
// drained: it drops escape sequences, keeping the text.
func stripQueries(b []byte) []byte { return []byte(ansi.Strip(string(b))) }

// view returns h rows of w cells: the bottom of the screen, cut to the width.
func (s *screenShot) view(w, h int) []string {
	top := max(0, s.bottom-h+1)
	var out []string
	for y := top; y < len(s.lines) && len(out) < h; y++ {
		out = append(out, renderCells(s.lines[y], w))
	}
	return out
}

// renderCells draws the first w cells of a line with their styles.
func renderCells(l uv.Line, w int) string {
	if w <= 0 {
		return ""
	}
	cut := make(uv.Line, 0, w)
	for x := 0; x < len(l) && x < w; x++ {
		c := l[x]
		if c.Width > 1 && x+c.Width > w {
			c = uv.EmptyCell
		}
		cut = append(cut, c)
	}
	return cut.Render() + ansi.ResetStyle
}

// previewLines fills the preview box.
func (m *Model) previewLines(w, h int) []string {
	p := m.preview
	switch {
	case p.key != m.sel || (p.shot == nil && p.loading):
		return []string{sKey.Render(m.spinner()) + " " + sFaint.Render("Loading the last screen…")}
	case p.shot == nil && p.err != "":
		var out []string
		for _, l := range wrap(sentence(p.err), w) {
			out = append(out, sFaint.Render(l))
		}
		return out
	case p.shot == nil:
		return []string{sFaint.Render("No screen to show.")}
	case p.shot.blank:
		return []string{sFaint.Render("No output yet.")}
	}
	return p.shot.view(w, h)
}
