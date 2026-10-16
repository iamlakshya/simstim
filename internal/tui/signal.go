package tui

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Signals (docs/contributing/ux-spec.md 2.3). Every session row ends in a trace: a small
// braille line of the session's output over the last half minute, scrolling
// as time passes. A busy agent draws a wave, a quiet one a flat line. The
// launcher learns it from each list: the time of the session's last output,
// and whether it is working.

const (
	traceCells  = 8
	traceWindow = 32 * time.Second
	traceKeep   = 40 * time.Second
	traceSpread = 1.7 // seconds either side of an event
)

// noteActivity records, for every listed session, a moment of output.
func (m *Model) noteActivity(h *hostConn, list []client.Session) {
	now := m.now()
	for _, s := range list {
		k := sessKey{h.name(), s.ID}
		ev := m.traces[k]
		last := time.Time{}
		if len(ev) > 0 {
			last = ev[len(ev)-1]
		}
		switch {
		case !s.LastActivityAt.IsZero() && s.LastActivityAt.After(last) && now.Sub(s.LastActivityAt) < traceKeep:
			ev = append(ev, s.LastActivityAt)
		case s.LastActivityAt.IsZero() && s.State == api.StateWorking && now.Sub(last) > time.Second:
			ev = append(ev, now) // no output times from this host: working is enough
		}
		for len(ev) > 0 && now.Sub(ev[0]) > traceKeep {
			ev = ev[1:]
		}
		m.traces[k] = ev
	}
}

// trace draws a session's signal, traceCells wide.
func (m *Model) trace(k sessKey, st lipgloss.Style) string {
	ev := m.traces[k]
	now := m.now()
	dots := traceCells * 2
	var b strings.Builder
	for c := range traceCells {
		r := 0
		for dx := range 2 {
			col := c*2 + dx
			t := now.Add(-time.Duration(float64(traceWindow) * float64(dots-1-col) / float64(dots-1)))
			v := 0.0
			for _, e := range ev {
				d := t.Sub(e).Seconds() / traceSpread
				v = math.Max(v, math.Exp(-d*d))
			}
			h := 1 + int(math.Round(v*3)) // the baseline dot, and up to three more
			for dy := range h {
				r |= 1 << brailleBit[3-dy][dx]
			}
		}
		b.WriteRune(rune(0x2800 + r))
	}
	return st.Render(b.String())
}

// Activity: one line about what the selected session is doing now, read
// from its screen.

type activityMsg struct {
	key  sessKey
	text string
}

// readActivity reads the selected session's screen once, while its list is
// on screen; the activity tick calls it now and then.
func (m *Model) readActivity() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil || h == nil || !h.online() || ended(*s) || m.modal != nil {
		return nil
	}
	key, c, ctx := sessKey{h.name(), s.ID}, h.conn, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		data, cols, rows, err := c.Peek(cctx, key.id, peekBytes)
		if err != nil {
			return nil
		}
		return activityMsg{key: key, text: activityLine(renderScreen(data, cols, rows))}
	}
}

func (m *Model) onActivity(msg activityMsg) {
	a := m.activity[msg.key]
	if msg.text != a.text {
		a.text, a.at = msg.text, m.now()
		m.activityAt = a.at
	}
	m.activity[msg.key] = a
}

type activity struct {
	text string
	at   time.Time // when it last changed, for its entrance
}

// chrome is text an agent always shows, which says nothing about its work.
// Tips count too: "⎿ Tip: …" under Claude Code's spinner, "Tip: …" in
// Codex. A tool's result under "⎿" says what the agent did, so it stays.
var chrome = regexp.MustCompile(`(?i)for shortcuts|bypass permissions|auto-accept|shift\+tab|^\s*[>❯›$#%]\s*$|^\s*\? |^\W*tip: `)

// activityLine is the line of a screen that best says what the agent is
// doing: the lowest line with words that is not a border, a prompt or a
// hint the agent always shows.
func activityLine(s *screenShot) string {
	if s == nil || s.blank {
		return ""
	}
	for y := s.bottom; y >= 0 && y < len(s.lines); y-- {
		text := strings.TrimSpace(strings.Map(func(r rune) rune {
			if r >= 0x2500 && r <= 0x257F { // box drawing
				return ' '
			}
			return r
		}, s.lines[y].String()))
		text = strings.Join(strings.Fields(text), " ")
		if text == "" || chrome.MatchString(text) {
			continue
		}
		letters := 0
		for _, r := range text {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				letters++
			}
		}
		if letters >= 4 && letters*2 >= len([]rune(text)) {
			return text
		}
	}
	return ""
}

type timeList = []time.Time

// mergeTimes joins several lists of moments into one, in order.
func mergeTimes(lists []timeList) timeList {
	var out timeList
	for _, l := range lists {
		out = append(out, l...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}
