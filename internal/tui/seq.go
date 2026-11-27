package tui

import (
	"hash/fnv"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Sequences (docs/contributing/ux-spec.md 4.5): short, choreographed moments that make a
// change easy to follow. None of them holds a key back for longer than the
// jack-in, and with motion off each one is its last frame.

const (
	decodeTime = 420 * time.Millisecond // text resolving out of noise
	ruleTime   = 260 * time.Millisecond // the header rule drawing itself
	slideTime  = 180 * time.Millisecond // a screen sliding in
	slideCols  = 8
	glideTime  = 110 * time.Millisecond // the selection's tint moving
	jackTime   = 170 * time.Millisecond // the sweep before an attach
	sheetTime  = 160 * time.Millisecond // a sheet rising
)

// noise is what decoding text shows before each letter settles.
var noise = []rune("⡏⢸⣿⠿⡿⢿⣻⣽⣾⣷⣯⣟⡷⢾⣼⣧")

// decode shows text resolving out of braille noise, letter by letter, from
// start. Spaces stay spaces.
func (m *Model) decode(text string, start time.Time) string {
	if !m.motion {
		return text
	}
	s := m.since(start)
	if s >= decodeTime {
		return text
	}
	runes := []rune(text)
	frame := int(m.since(m.epoch) / fastFrame)
	var b strings.Builder
	for i, r := range runes {
		settle := time.Duration(float64(decodeTime) * float64(i+1) / float64(len(runes)+1))
		switch {
		case r == ' ' || s >= settle:
			b.WriteRune(r)
		case s < settle-decodeTime/2:
			b.WriteRune(' ')
		default:
			h := fnv.New32a()
			// Only a hash: the bytes may wrap, and noise is a short, fixed list.
			_, _ = h.Write([]byte{byte(i), byte(frame), byte(frame >> 8)}) //nolint:gosec // G115: see above
			b.WriteRune(noise[h.Sum32()%uint32(len(noise))])               //nolint:gosec // G115: see above
		}
	}
	return b.String()
}

// ruleLen is how much of the header rule has drawn itself.
func (m *Model) ruleLen(w int) int {
	if !m.motion {
		return w
	}
	return int(math.Round(ease(float64(m.since(m.epoch))/float64(ruleTime)) * float64(w)))
}

// slide shifts a row as a new screen slides in: from the right going
// deeper, from the left coming back.
func (m *Model) slide(row string, w int) string {
	if !m.motion || m.slideDir == 0 {
		return row
	}
	p := ease(float64(m.since(m.slideAt)) / float64(slideTime))
	dx := int(math.Round((1 - p) * slideCols))
	if dx == 0 {
		return row
	}
	if m.slideDir > 0 {
		return ansi.Truncate(strings.Repeat(" ", dx)+row, w, "")
	}
	return ansi.TruncateLeft(row, dx, "")
}

// glide is how far the selection's tint has moved to the new row.
func (m *Model) glide() float64 {
	if !m.motion {
		return 1
	}
	return ease(float64(m.since(m.selAt)) / float64(glideTime))
}

// The jack-in: a short sweep before an attach hands the terminal over.

type jackMsg struct{}

type jackIn struct {
	h        *hostConn
	id, name string
	at       time.Time
}

// jack starts the sweep, then attaches. With motion off it attaches at once.
func (m *Model) jack(h *hostConn, id, name string) tea.Cmd {
	if m.transferInputBlocked() {
		return nil
	}
	if !m.motion {
		return m.attachTo(h, id, name)
	}
	m.jacking = &jackIn{h: h, id: id, name: name, at: m.now()}
	return m.after(jackTime, jackMsg{})
}

func (m *Model) onJack() tea.Cmd {
	j := m.jacking
	m.jacking = nil
	if j == nil {
		return nil
	}
	return m.attachTo(j.h, j.id, j.name)
}

// sheetRise is how many rows of a sheet are still below the screen's edge.
func (m *Model) sheetRise(h int) int {
	if !m.motion {
		return 0
	}
	p := ease(float64(m.since(m.modalAt)) / float64(sheetTime))
	return int(math.Round((1 - p) * float64(h)))
}

// tint washes a row with a little of the accent colour, alpha 0 to 1. It
// needs the terminal's colours; without them the marker alone shows the
// selection.
func (m *Model) tint(c *lipgloss.Canvas, x, y, w int, alpha float64) {
	if alpha <= 0 || !m.smooth() {
		return
	}
	acc, ok := m.colors.resolve(colAccent, true)
	if !ok {
		return
	}
	back := bg(m)
	wash := mix(back, acc, 0.13*alpha).color()
	for xx := x; xx < x+w; xx++ {
		if cell := c.CellAt(xx, y); cell != nil && cell.Style.Bg == nil {
			cell.Style.Bg = wash
		}
	}
}

// sweep draws the jack-in on a canvas: the list fades away from the chosen
// row, and a bright line runs down from it.
func (m *Model) sweep(c *lipgloss.Canvas, g boxGeo, row int) {
	if m.jacking == nil {
		return
	}
	p := ease(float64(m.since(m.jacking.at)) / float64(jackTime))
	for i := range g.listN {
		if i != row {
			m.fade(c, g.x, g.listY+i, g.w, 1-p)
		}
	}
	y := g.listY + row + int(math.Round(p*float64(max(m.height-2-g.listY-row, 1))))
	if y < m.height && y > g.listY+row {
		drawAt(c, sAccent.Bold(true).Render(strings.Repeat("━", g.w)), g.x, y)
	}
}
