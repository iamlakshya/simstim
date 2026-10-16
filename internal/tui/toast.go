package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

type toastKind int

const (
	toastInfo toastKind = iota
	toastSuccess
	toastWarning
	toastError
)

// maxToasts is how many toasts show at once. More wait in a queue.
const maxToasts = 3

type toast struct {
	kind       toastKind
	text, hint string
	life       time.Duration
	at, until  time.Time // shown at, gone at
}

type toastTickMsg struct{}

// toastStack holds the visible toasts and the queue behind them.
type toastStack struct {
	shown []toast
	queue []toast
}

func lifeOf(k toastKind) time.Duration {
	if k == toastError {
		return toastErrorLife
	}
	return toastLife
}

// push adds a toast. A repeat of a visible one extends it instead. A toast
// with a step to try lasts as long as an error, so there is time to read it.
func (t *toastStack) push(now time.Time, kind toastKind, text, hint string) {
	life := lifeOf(kind)
	if hint != "" {
		life = toastErrorLife
	}
	for i := range t.shown {
		if t.shown[i].text == text {
			t.shown[i].until = now.Add(life)
			return
		}
	}
	for _, q := range t.queue {
		if q.text == text {
			return
		}
	}
	nt := toast{kind: kind, text: text, hint: hint, life: life, at: now, until: now.Add(life)}
	if len(t.shown) < maxToasts {
		t.shown = append(t.shown, nt)
	} else {
		t.queue = append(t.queue, nt)
	}
}

// expire drops the toasts whose time is up and shows queued ones. It reports
// whether anything is left to time.
func (t *toastStack) expire(now time.Time) bool {
	kept := t.shown[:0]
	for _, x := range t.shown {
		if now.Before(x.until) {
			kept = append(kept, x)
		}
	}
	t.shown = kept
	for len(t.shown) < maxToasts && len(t.queue) > 0 {
		q := t.queue[0]
		t.queue = t.queue[1:]
		q.at, q.until = now, now.Add(q.life)
		t.shown = append(t.shown, q)
	}
	return len(t.shown) > 0
}

// next is the time until the first visible toast expires.
func (t *toastStack) next(now time.Time) (time.Duration, bool) {
	if len(t.shown) == 0 {
		return 0, false
	}
	first := t.shown[0].until
	for _, x := range t.shown[1:] {
		if x.until.Before(first) {
			first = x.until
		}
	}
	return max(first.Sub(now), 10*time.Millisecond), true
}

func (m *Model) toast(kind toastKind, text string) tea.Cmd {
	m.toasts.push(m.now(), kind, text, "")
	return m.after(lifeOf(kind), toastTickMsg{})
}

// toastErr shows an error with its "Try:" line.
func (m *Model) toastErr(err error) tea.Cmd {
	msg, hint := errText(err)
	m.toasts.push(m.now(), toastError, sentence(msg), hint)
	return m.after(toastErrorLife, toastTickMsg{})
}

// toastNext shows a notice with the step to try next under it.
func (m *Model) toastNext(kind toastKind, text, hint string) tea.Cmd {
	m.toasts.push(m.now(), kind, text, hint)
	return m.after(toastErrorLife, toastTickMsg{})
}

// nextLines draws the step to try next: a cyan arrow, then the step.
func nextLines(hint string, w int) []string {
	lines := wrap(hint, w-2)
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		lead := sAccent.Render(glyphNext) + " "
		if i > 0 {
			lead = "  "
		}
		out = append(out, lead+sLink.Render(l))
	}
	return out
}

// noticeLine is one line of a notice, how visible it is as it fades, and
// when it came, for its slide.
type noticeLine struct {
	s     string
	alpha float64
	at    time.Time
}

// noticeLines draws the toasts as short lines that rise above the bottom
// rule, the newest lowest: a glyph and the text, or for an error a red cross
// and the step to try next (docs/contributing/ux-spec.md 2.8).
func (m *Model) noticeLines(w int) []noticeLine {
	var out []noticeLine
	for _, t := range m.toasts.shown {
		var lines []string
		switch t.kind {
		case toastError:
			lines = errorBlock(t.text, t.hint, w)
		default:
			glyph, st, text := glyphInfo, sAccent, sFaint
			switch t.kind {
			case toastSuccess:
				glyph, st, text = glyphOK, sSuccess, sPlain
			case toastWarning:
				glyph, st, text = glyphNeedsInput, sWarning, sPlain
			default:
				// Info keeps the defaults above; errors are drawn in the outer switch.
			}
			for i, l := range wrap(t.text, w-2) {
				lead := "  "
				if i == 0 {
					lead = st.Render(glyph) + " "
				}
				lines = append(lines, lead+text.Render(l))
			}
			if t.hint != "" {
				lines = append(lines, nextLines(t.hint, w)...)
			}
		}
		a := m.noticeAlpha(t)
		for _, l := range lines {
			out = append(out, noticeLine{l, a, t.at})
		}
	}
	if len(out) > noteRows {
		out = out[len(out)-noteRows:]
	}
	return out
}
