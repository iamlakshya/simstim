package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher (docs/contributing/ux-spec.md 2.2 and 2.3) is one list in one column, with no
// box round it: a header with the brand and where you are, a rule, the
// list, a line about the selected row, and at the bottom a rule, the prompt
// line and the keys. Notices rise just above the bottom rule.

// boxGeo is where the launcher's parts were last drawn: for the motion and
// for the mouse.
type boxGeo struct {
	x, y, w, h   int
	listY, listN int // the first list row on screen, and how many are drawn
	top          int // the list index of the first row drawn
	detailY      int // the line under the list, or 0
	sel          int // the selected row, as drawn, or -1
	from         int // the row the selection came from, as drawn, or -1
}

const (
	headerY  = 1
	ruleY    = 2
	listTop  = 4
	footRows = 3 // the bottom rule, the prompt line and the keys
	noteRows = 2 // notices above the bottom rule
)

// cw is the width of the launcher's column, and boxW the same: the old
// name, kept for the sheets.
func (m *Model) cw() int   { return max(min(m.width-4, 116), min(m.width, 30)) }
func (m *Model) boxW() int { return m.cw() }

// x0 is the left edge of the column.
func (m *Model) x0() int { return max((m.width-m.cw())/2, 0) }

// listRoom is how many rows the list may take.
func (m *Model) listRoom() int { return max(m.height-listTop-footRows-noteRows-2, 1) }

// listView is what one screen puts in the column.
type listView struct {
	crumb, facts string
	rows         []string
	listed       bool // rows are list rows, not an empty state
	top, sel     int
	detail       string
	hints        []hint
}

// viewLauncher draws the launcher. It records where things are in m.geo.
func (m *Model) viewLauncher() string {
	cw, x := m.cw(), m.x0()
	var v listView
	switch {
	case m.prompt != nil && m.prompt.mode == promptSearch:
		v = m.searchView(cw)
	case m.screen == scrHosts:
		v = m.hostsView(cw)
	default:
		v = m.sessionsView(cw)
	}
	switch {
	case m.rename != nil:
		v.hints = []hint{{"enter", "rename"}, {"esc", "cancel"}}
	case m.prompt != nil && m.prompt.mode == promptSearch:
		v.hints = []hint{{"enter", "attach"}, {"↑↓", "move"}, {"esc", "close"}}
	case m.prompt != nil:
		v.hints = []hint{{"enter", "start"}, {"tab", "complete"}, {"ctrl+enter", "detached"}, {"ctrl+f", "form"}, {"esc", "cancel"}}
	}
	lines := make([]string, m.height)
	put := func(y int, s string) {
		if y >= 0 && y < m.height {
			lines[y] = strings.Repeat(" ", x) + s
		}
	}
	// The header: the logo, the name, where you are; facts on the right.
	left := m.logo() + " " + m.brandName() + "  " + v.crumb
	put(headerY, left+strings.Repeat(" ", max(cw-cells(left)-cells(v.facts), 1))+v.facts)
	put(ruleY, sFaint.Render(strings.Repeat("─", m.ruleLen(cw))))

	m.geo = boxGeo{x: x, y: listTop, w: cw, listY: listTop, top: v.top, sel: -1, from: -1}
	for i, r := range v.rows {
		if listTop+i >= m.height-footRows-noteRows {
			break
		}
		put(listTop+i, m.slide(r, cw))
	}
	if v.listed {
		m.geo.listN = len(v.rows)
		if v.sel >= v.top && v.sel < v.top+len(v.rows) {
			m.geo.sel = v.sel - v.top
		}
		if f := m.selFrom - v.top; f >= 0 && f < len(v.rows) && f != m.geo.sel {
			m.geo.from = f
		}
	}
	if v.detail != "" {
		m.geo.detailY = listTop + len(v.rows) + 1
		put(m.geo.detailY, m.slide(v.detail, cw))
	}
	// The bottom: a rule, the prompt line, the keys; while a session is
	// being started, its choices just above the rule.
	if q := m.quickHints(cw - 2); q != "" {
		put(m.height-4, "  "+q)
	}
	put(m.height-3, sFaint.Render(strings.Repeat("─", cw)))
	put(m.height-2, "  "+m.promptLine(cw-2))
	put(m.height-1, "  "+m.footer(v.hints, cw-2))
	m.geo.h = m.height - listTop
	return strings.Join(lines, "\n")
}

// footer is the keys, or what the quit chord does next.
func (m *Model) footer(hs []hint, w int) string {
	if m.quitArmed {
		return sDanger.Bold(true).Render("ctrl+c") + sFaint.Render(" again to quit, sessions keep running") + sep + sKey.Render("esc") + sFaint.Render(" stay")
	}
	return hintLine(hs, w)
}

// logo is the brand's signal: three braille cells of a small wave in the
// brand gradient. With motion on, the wave runs on the logo's clock, so it
// moves one step with each frame tick and stays still between them.
func (m *Model) logo() string {
	t := 0.0
	if m.motion {
		t = m.logoClock.Seconds()
	}
	cols := m.gradient(3)
	var b strings.Builder
	for c := range 3 {
		r := 0
		for dx := range 2 {
			x := float64(c*2 + dx)
			y := int(math.Round(1.5 + 1.5*math.Sin(x*0.9-t*4)))
			r |= 1 << brailleBit[min(max(y, 0), 3)][dx]
		}
		st := sBrand.Bold(true)
		if cols != nil {
			st = lipgloss.NewStyle().Foreground(cols[c]).Bold(true)
		}
		b.WriteString(st.Render(string(rune(0x2800 + r))))
	}
	return b.String()
}

// brandName is the name in the brand gradient, bold.
func (m *Model) brandName() string {
	name := []rune(brand.Name)
	cols := m.gradient(len(name))
	if cols == nil {
		return sBrand.Bold(true).Render(brand.Name)
	}
	var b strings.Builder
	for i, r := range name {
		b.WriteString(lipgloss.NewStyle().Foreground(cols[i]).Bold(true).Render(string(r)))
	}
	return b.String()
}

// gutter is the row's number, faint, for the first nine rows: 1–9 open them.
func gutter(n int) seg {
	if n >= 1 && n <= 9 {
		return seg{strconv.Itoa(n), sFaint}
	}
	return seg{" ", sPlain}
}

// nameSegs is a name w cells wide, with the letters that match q in the
// accent colour.
func nameSegs(name string, w int, st lipgloss.Style, q string) []seg {
	name = trunc(name, w)
	pad := seg{strings.Repeat(" ", max(w-cells(name), 0)), sPlain}
	hit := fuzzyPos(q, name)
	if len(hit) == 0 {
		return []seg{{name, st}, pad}
	}
	var out []seg
	for i, r := range []rune(name) {
		s := st
		if hit[i] {
			s = sKeyBold.Underline(true)
		}
		if n := len(out); n > 0 && out[n-1].st.GetUnderline() == s.GetUnderline() {
			out[n-1].s += string(r)
			continue
		}
		out = append(out, seg{string(r), s})
	}
	return append(out, pad)
}

// Sessions.

func (m *Model) sessionsView(cw int) listView {
	v := listView{hints: []hint{{"enter", "attach"}, {"space", "peek"}, {"r", "rename"}, {"esc", "hosts"}, {"ctrl+o", "commands"}, {"?", "keys"}}}
	h := m.selectedHost()
	if h == nil {
		v.crumb, v.facts = sFaint.Render("all sessions"), m.needFacts(sFaint.Render(plural(len(m.hosts), "host", "hosts")))
	} else {
		v.crumb = sFaint.Render("› ") + sBold.Render(m.decode(h.name(), m.revealAt))
		v.facts = m.hostState(h)
	}
	rows := m.rows()
	if sessionRows(rows) == 0 {
		// With no session to act on, offer only what works here.
		v.hints = []hint{{"n", "new session"}, {"esc", "hosts"}, {"ctrl+o", "commands"}, {"?", "keys"}}
		if h != nil && (h.status == stOffline || h.status == stFailed) {
			v.hints = []hint{{"esc", "hosts"}, {"i", "details"}, {"ctrl+o", "commands"}, {"?", "keys"}}
		}
		for _, l := range m.emptyState(cw - 2) {
			v.rows = append(v.rows, "  "+l)
		}
		return v
	}
	cols := sessCols(cw - 2)
	n := 0
	for i := range m.sessTop {
		if rows[i].kind == rowSession {
			n++
		}
	}
	v.listed, v.top, v.sel = true, m.sessTop, m.selIdx
	for i := m.sessTop; i < len(rows) && i < m.sessTop+m.listRoom(); i++ {
		if rows[i].kind == rowSession {
			n++
		}
		v.rows = append(v.rows, marker(i == m.selIdx && rows[i].kind == rowSession)+m.sessRowView(rows[i], n, cols, i == m.selIdx, false))
	}
	if hc, s := m.selectedPair(); s != nil {
		v.detail = "  " + m.sessionDetail(hc, *s, cw-2, false)
	}
	return v
}

// searchView lists the sessions of every host that match the search.
func (m *Model) searchView(cw int) listView {
	v := listView{crumb: sFaint.Render("› ") + sBold.Render("search"), facts: sFaint.Render("every host")}
	rs := m.results()
	p := m.prompt
	p.sel = min(p.sel, max(len(rs)-1, 0))
	if len(rs) == 0 {
		v.rows = []string{"  " + sFaint.Render(trunc(fmt.Sprintf("Nothing matches '%s'.", p.in.value()), cw-2))}
		return v
	}
	cols := sessCols(cw - 2)
	room := m.listRoom()
	top := max(0, p.sel-room+1)
	v.listed, v.top, v.sel = true, top, p.sel
	for i := top; i < len(rs) && i < top+room; i++ {
		v.rows = append(v.rows, marker(i == p.sel)+m.sessRowView(rs[i], i+1, cols, i == p.sel, true))
	}
	if r := rs[p.sel]; true {
		v.detail = "  " + m.sessionDetail(r.h, r.s, cw-2, cols.folder == 0)
	}
	return v
}

// hostState is the fact at the right of a host's sessions.
func (m *Model) hostState(h *hostConn) string {
	switch h.status {
	case stConnecting:
		return sAccent.Render(m.spinner()) + " " + m.shimmer("connecting")
	case stOffline:
		return sFaint.Render("offline")
	case stFailed:
		return sDanger.Render("not ready")
	default:
		// Online: the latency, the sessions and who needs you, below.
	}
	facts := sFaint.Render(fmtLatency(h.latency) + dot + plural(len(h.sessions), "session", "sessions"))
	need := 0
	for _, s := range h.sessions {
		if s.State == "needs_input" {
			need++
		}
	}
	if need > 0 {
		facts += sFaint.Render(dot) + sWarning.Render(fmt.Sprintf("%d needs you", need))
	}
	return facts
}

// Column widths of a session row.
const (
	colAgent = 8
	colState = 12
	colAge   = 4
)

// minFolder is the narrowest folder column worth showing. Below it the
// column goes, and the line under the list still shows the folder.
const minFolder = 8

// sessColumns is the width of the columns that share the free space. A
// folder of 0 means the column is hidden.
type sessColumns struct {
	name, folder int
}

func sessCols(cw int) sessColumns {
	fixed := 4 + 2 + colAgent + 2 + colState + 2 + colAge + 2 + traceCells // all but the folder
	rest := cw - fixed - 2                                                 // name and folder, after the folder's gap
	c := sessColumns{name: min(max(rest*45/100, 10), 28)}
	c.folder = rest - c.name
	if c.folder < minFolder {
		c = sessColumns{name: max(cw-fixed, 6)}
	}
	return c
}

func (m *Model) sessRowView(r sessRow, n int, c sessColumns, selected, withHost bool) string {
	switch r.kind {
	case rowHeader:
		return sBold.Render(r.h.name()) + "  " + m.hostState(r.h)
	case rowNote:
		msg, _ := errText(r.h.err)
		if msg == "" {
			msg = "not ready"
		}
		return "  " + sDanger.Render(glyphFail) + " " + sFaint.Render(trunc(sentence(msg), c.name+c.folder+20))
	default:
		// A session row is drawn by the caller.
	}
	s := r.s
	glyph, label := m.look(s)
	traceSt := label.st
	if !r.h.loaded {
		// Remembered from the last run: shown at once, quiet until the host
		// answers.
		glyph.st, label.st, traceSt = sFaint, sFaint, sFaint
	}
	q := ""
	if m.prompt != nil && m.prompt.mode == promptSearch {
		q = m.prompt.in.value()
	}
	name := nameSegs(s.Name, c.name, sBold, q)
	if m.rename != nil && m.rename.key == r.key() {
		name = []seg{{fit(m.rename.in.view(c.name, true), c.name), sPlain}}
	}
	where := truncPath(homePath(r.h, s.Cwd), c.folder)
	if withHost {
		where = trunc(r.h.name()+":"+homePath(r.h, s.Cwd), c.folder)
	}
	segs := []seg{gutter(n), {" ", sPlain}, glyph, {" ", sPlain}}
	segs = append(segs, name...)
	segs = append(segs, seg{"  ", sPlain}, seg{fit(s.Agent, colAgent), sFaint}, seg{"  ", sPlain})
	if c.folder > 0 {
		segs = append(segs, seg{fit(where, c.folder), sFaint}, seg{"  ", sPlain})
	}
	segs = append(segs,
		seg{fit(label.s, colState), label.st}, seg{"  ", sPlain},
		seg{fitRight(fmtAge(m.now().Sub(since(s))), colAge), sFaint}, seg{"  ", sPlain},
		seg{m.trace(r.key(), traceSt), sPlain})
	return listRow(selected, segs...)
}

// sessionDetail is the line under the sessions: what the selected one is
// doing now, read from its screen, or what it waits for; then where it runs
// and its memory.
func (m *Model) sessionDetail(h *hostConn, s client.Session, cw int, withHost bool) string {
	var now string
	switch a := m.activity[sessKey{h.name(), s.ID}]; {
	case s.State == "needs_input" && s.Reason != "":
		now = sWarning.Render("● " + s.Reason)
	case len(s.MissingSecrets) > 0 && s.Alive:
		// After a host restart the agent runs without the secrets it had.
		now = sWarning.Render("● started without its secrets: " + strings.Join(s.MissingSecrets, ", "))
	case a.text != "" && s.Alive:
		now = sAccent.Render("▸ ") + m.decode(a.text, a.at)
	}
	where := homePath(h, s.Cwd)
	if withHost {
		where = h.name() + ":" + where
	}
	var facts []string
	for _, p := range []string{where, fmtMemory(s.MemoryCurrent, s.MemoryMax), s.ID} {
		if p != "" {
			facts = append(facts, p)
		}
	}
	if !h.loaded {
		facts = append(facts, "last seen before this run")
	}
	if now == "" {
		return sFaint.Render(trunc(strings.Join(facts, dot), cw))
	}
	right := sFaint.Render(trunc(strings.Join(facts, dot), cw/2))
	room := cw - cells(right) - 3
	return trunc(now, room) + strings.Repeat(" ", max(room-cells(trunc(now, room)), 0)+3) + right
}

// emptyState explains an empty sessions list, and what to do next.
func (m *Model) emptyState(cw int) []string {
	h := m.selectedHost()
	if h == nil {
		loaded, connecting := false, false
		for _, x := range m.hosts {
			loaded = loaded || x.loaded
			connecting = connecting || x.status == stConnecting
		}
		var out []string
		switch {
		case loaded && m.hideExited:
			out = append(out, sFaint.Render("No running sessions."), "", keyHint(optKey+"+e", "show ended sessions"))
		case loaded:
			out = append(out, sFaint.Render("No sessions yet."), "", keyHint("esc", "pick a host, then n for a new session"))
		case connecting:
			out = append(out, sAccent.Render(m.spinner())+" "+m.shimmer("Connecting…"))
		}
		for _, x := range m.hosts {
			if x.status == stOffline || x.status == stFailed {
				out = append(out, sFaint.Render(trunc(glyphOffline+" "+x.name()+" is offline.", cw)))
			}
		}
		return out
	}
	switch {
	case h.status == stOffline || h.status == stFailed:
		return m.hostError(h, cw)
	case !h.loaded:
		return []string{sAccent.Render(m.spinner()) + " " + m.shimmer(trunc("Connecting to "+h.name()+"…", cw-2))}
	case len(h.sessions) > 0 && m.hideExited:
		return []string{sFaint.Render("No running sessions."), "", keyHint(optKey+"+e", "show ended sessions")}
	default:
		return []string{sFaint.Render(trunc("No sessions on "+h.name()+" yet.", cw)), "", keyHint("n", "start one from a single line, like: claude ~/src/app")}
	}
}

// keyHint is the faint hint of an empty list: "n new session".
func keyHint(key, desc string) string {
	return sKey.Render(key) + sFaint.Render(" "+desc)
}

// hostError says why a host cannot be used, and what to try.
func (m *Model) hostError(h *hostConn, cw int) []string {
	msg, hint := errText(h.err)
	if msg == "" {
		msg = h.name() + " is offline"
	}
	if hint == "" {
		hint = fmt.Sprintf("%s host test %s", brand.Name, h.name())
	}
	return append(errorBlock(msg, hint, cw), "", sFaint.Render("Retrying in the background."))
}

// errorBlock is an error and the step to try next, wrapped to w cells: a red
// cross and the message, then an arrow and the step.
func errorBlock(msg, hint string, w int) []string {
	var out []string
	for i, l := range wrap(sentence(msg), w-2) {
		lead := sErrHead.Render(glyphFail) + " "
		if i > 0 {
			lead = "  "
		}
		out = append(out, lead+l)
	}
	if hint != "" {
		for i, l := range wrap(hint, w-2) {
			lead := sAccent.Render(glyphNext) + " "
			if i > 0 {
				lead = "  "
			}
			out = append(out, lead+sLink.Render(l))
		}
	}
	return out
}
