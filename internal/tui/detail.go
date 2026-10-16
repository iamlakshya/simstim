package tui

import (
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// The line under the sessions (docs/contributing/ux-spec.md 2.4) is about
// the selected session. At the left: why an ended session ended and what to
// do next, or what a running one waits for or is doing now. At the right:
// where it runs, its memory while the agent runs, a note when the memory
// cap stopped another process, and its ID.

// capNote is the note at the right when the memory cap stopped a process
// in the session other than the agent (client.ChildStopped).
const capNote = "a process hit the cap"

// minFacts is how many cells of the facts at the right the keys among the
// next steps leave.
const minFacts = 12

// sessionDetail is the line under the sessions, cw cells wide.
func (m *Model) sessionDetail(h *hostConn, s client.Session, cw int, withHost bool) string {
	now, steps := m.sessionNow(h, s)
	where := homePath(h, s.Cwd)
	if withHost {
		where = h.name() + ":" + where
	}
	mem, note := "", ""
	if !ended(s) {
		// After the agent ends, what the session's unit holds is not the
		// agent's: the holder, any process the agent left, and page cache.
		mem = fmtMemory(s.MemoryCurrent, s.MemoryMax)
	}
	if client.ChildStopped(s) {
		// The cap stopped another process in the session, not the agent.
		note = capNote
	}
	empty := func(p string) bool { return p == "" }
	head := slices.DeleteFunc([]string{where, mem}, empty)
	tail := slices.DeleteFunc([]string{s.ID}, empty)
	if !h.loaded {
		tail = append(tail, "last seen before this run")
	}
	if now == "" {
		return factsLine(head, note, tail, cw)
	}
	rightW := cw / 2
	if len(steps) > 0 && m.prompt == nil && m.rename == nil {
		// The next steps include keys, so they show only while keys reach
		// the list. They come before the facts.
		keep := min(cells(strings.Join(slices.Concat(head, tail), dot)), minFacts)
		if left, ok := withSteps(now, steps, cw-3, keep); ok {
			now, rightW = left, cw-3-cells(left)
		}
	}
	if note != "" {
		// The note shows only when the left still fits whole: it never cuts
		// a question, an activity or a step. Without room, it goes before the
		// folder is cut.
		if w := cw - 3 - cells(now); cells(strings.Join(slices.Concat(head, []string{note}), dot)) <= w {
			rightW = w
		} else {
			note = ""
		}
	}
	right := factsLine(head, note, tail, rightW)
	room := cw - cells(right) - 3
	now = trunc(now, room)
	return now + strings.Repeat(" ", max(room-cells(now), 0)+3) + right
}

// sessionNow is the left of the line under the sessions, and the next steps
// that go with it.
func (m *Model) sessionNow(h *hostConn, s client.Session) (string, []hint) {
	if ended(s) {
		glyph, label := m.look(s)
		return glyph.st.Render(glyph.s) + " " + label.st.Render(endWhy(s)), endedSteps(h, s)
	}
	switch a := m.activity[sessKey{h.name(), s.ID}]; {
	case s.State == api.StateNeedsInput && s.Reason != "":
		return sWarning.Render("● " + s.Reason), nil
	case len(s.MissingSecrets) > 0:
		// After a host restart the agent runs without the secrets it had.
		return sWarning.Render("● started without its secrets: " + strings.Join(s.MissingSecrets, ", ")), nil
	case a.text != "":
		return sAccent.Render("▸ ") + m.decode(a.text, a.at), nil
	}
	return "", nil
}

// withSteps adds to the reason the next steps that fit whole in w cells, in
// order. A key leaves keep cells for the facts at the right. A command may
// take their room too: the line is the one place that shows it with the
// reason. A step that does not fit is left out, and a later one may still
// fit. It reports whether any step fits.
func withSteps(reason string, steps []hint, w, keep int) (string, bool) {
	out, ok := reason, false
	for _, st := range steps {
		room := w - keep
		if st.key == glyphNext {
			room = w
		}
		if next := out + sep + stepText(st); cells(next) <= room {
			out, ok = next, true
		}
	}
	return out, ok
}

// factsLine is the right of the line under the sessions, at most w cells:
// the head and the tail, faint, with the note in yellow between them. The
// note shows whole or not at all.
func factsLine(head []string, note string, tail []string, w int) string {
	lead := strings.Join(head, dot)
	if lead != "" {
		lead += dot
	}
	if note == "" || cells(lead+note) > w {
		return sFaint.Render(trunc(strings.Join(slices.Concat(head, tail), dot), w))
	}
	var b strings.Builder
	b.WriteString(sFaint.Render(lead) + sWarning.Render(note))
	n := cells(lead + note)
	for _, t := range tail {
		if n+cells(dot+t) > w {
			break
		}
		b.WriteString(sFaint.Render(dot + t))
		n += cells(dot + t)
	}
	return b.String()
}
