package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// Hosts.

func (m *Model) hostsView(cw int) listView {
	v := listView{
		crumb: sFaint.Render("hosts"),
		facts: m.needFacts(sFaint.Render(plural(len(m.hosts), "host", "hosts"))),
		hints: []hint{{"enter", "open"}, {"1–9", "jump"}, {"a", "add host"}, {"ctrl+o", "commands"}, {"?", "keys"}},
	}
	vh := m.visibleHosts()
	nameW := 8
	for _, h := range vh {
		nameW = max(nameW, cells(h.name()))
	}
	nameW = min(nameW, max(cw/4, 8))
	rttW := hostRight
	for _, h := range vh {
		if h.online() {
			rttW = max(rttW, cells(h.rtt.text()))
		}
	}
	v.listed, v.top, v.sel = true, m.hostTop, m.hostSel
	for i := m.hostTop; i < len(vh) && i < m.hostTop+m.listRoom(); i++ {
		v.rows = append(v.rows, marker(i == m.hostSel)+m.hostRow(vh[i], i+1, i == m.hostSel, cw-2, nameW, rttW))
	}
	if h := m.selectedHost(); h != nil {
		v.detail = "  " + m.hostDetail(h, cw-2)
	}
	return v
}

// needFacts adds "1 needs you" to the facts, in yellow, when it is so.
func (m *Model) needFacts(facts string) string {
	if n := m.needYou(); n > 0 {
		return facts + sFaint.Render(dot) + sWarning.Render(fmt.Sprintf("%d needs you", n))
	}
	return facts
}

// hostRight is the narrowest round-trip column of the hosts list. The
// column grows to fit the widest round trip that it shows.
const hostRight = 6

// hostRow is one host in the list: its state, its name, its sessions, its
// round trip in a column rttW cells wide, and its trace.
func (m *Model) hostRow(h *hostConn, n int, selected bool, cw, nameW, rttW int) string {
	detailW := cw - 4 - nameW - 2 - 2 - rttW - 2 - traceCells
	glyph := seg{glyphOnline, sSuccess}
	nameSt := sBold
	var detail []seg
	right := append([]seg{{strings.Repeat(" ", max(rttW-cells(h.rtt.text()), 0)), sPlain}}, h.rtt.segs()...)
	blank := []seg{{strings.Repeat(" ", rttW), sPlain}}
	switch h.status {
	case stConnecting:
		glyph, right = seg{m.spinner(), sAccent}, blank
		text := "connecting…"
		if len(h.sessions) > 0 {
			text = m.summaryText(h) + dot + "connecting…"
		}
		detail = []seg{{text, sFaint}}
		if !selected {
			detail = []seg{{m.shimmer(text), sPlain}}
		}
	case stOffline:
		glyph, nameSt, right = seg{glyphOffline, sFaint}, sFaint, blank
		detail = []seg{{"offline", sFaint}}
	case stFailed:
		glyph, right = seg{glyphFail, sDanger}, blank
		detail = []seg{{"not ready", sDanger}}
	default:
		detail = m.hostSummary(h, detailW)
	}
	used := 0
	for i := range detail {
		detail[i].s = trunc(detail[i].s, detailW-used)
		used += cells(detail[i].s)
	}
	name := nameSegs(h.name(), nameW, nameSt, "")
	segs := make([]seg, 0, 10+len(name)+len(detail)+len(right))
	segs = append(segs, gutter(n), seg{" ", sPlain}, glyph, seg{" ", sPlain})
	segs = append(segs, name...)
	segs = append(segs, seg{"  ", sPlain})
	segs = append(segs, detail...)
	segs = append(segs, seg{strings.Repeat(" ", max(detailW-used, 0)), sPlain}, seg{"  ", sPlain})
	segs = append(segs, right...)
	segs = append(segs, seg{"  ", sPlain}, seg{m.hostTrace(h), sPlain})
	return listRow(selected, segs...)
}

// hostTrace is the trace of all of a host's sessions together.
func (m *Model) hostTrace(h *hostConn) string {
	k := sessKey{h.name(), ""}
	all := make([]timeList, 0, len(h.sessions))
	for _, s := range h.sessions {
		all = append(all, m.traces[sessKey{h.name(), s.ID}])
	}
	m.traces[k] = mergeTimes(all)
	st := sFaint
	if h.online() && len(m.traces[k]) > 0 {
		st = sAccent
	}
	return m.trace(k, st)
}

// summaryText is a host's sessions in a few words: "4 sessions".
func (m *Model) summaryText(h *hostConn) string {
	if len(h.sessions) == 0 {
		return "no sessions"
	}
	return plural(len(h.sessions), "session", "sessions")
}

// hostSummary is the faint line about a host's sessions; the count that
// needs the user stands out. When the two do not fit in w cells, the
// session count goes, as in the header, so that the other stays whole.
func (m *Model) hostSummary(h *hostConn, w int) []seg {
	if !h.loaded && len(h.sessions) == 0 {
		return []seg{{"…", sFaint}}
	}
	out := []seg{{m.summaryText(h), sFaint}}
	if need := needCount(h); need > 0 {
		needs := seg{fmt.Sprintf("%d needs you", need), sWarning}
		if cells(out[0].s+dot+needs.s) > w {
			return []seg{needs}
		}
		out = append(out, seg{dot, sFaint}, needs)
	}
	return out
}

// needCount counts h's sessions that need the user.
func needCount(h *hostConn) int {
	n := 0
	for _, s := range h.sessions {
		if s.State == api.StateNeedsInput {
			n++
		}
	}
	return n
}

// hostState is the facts at the right of a host's sessions. Online, they
// are its round trip, its sessions and who needs you, in at most w cells
// when they can be: the session count goes first, then the slow round trip.
func (m *Model) hostState(h *hostConn, w int) string {
	switch h.status {
	case stConnecting:
		return sAccent.Render(m.spinner()) + " " + m.shimmer("connecting")
	case stOffline:
		return sFaint.Render("offline")
	case stFailed:
		return sDanger.Render("not ready")
	default:
		// Online: below.
	}
	var need string
	if n := needCount(h); n > 0 {
		need = sWarning.Render(fmt.Sprintf("%d needs you", n))
	}
	count := sFaint.Render(plural(len(h.sessions), "session", "sessions"))
	rtt, short := renderSegs(h.rtt.segs()), ""
	if h.rtt.n > 0 {
		short = sFaint.Render(fmtLatency(h.rtt.mid))
	}
	for _, f := range [][]string{{rtt, count, need}, {rtt, need}, {short, need}, {need}} {
		if facts := joinFacts(f...); cells(facts) <= w {
			return facts
		}
	}
	return ""
}

// joinFacts joins the facts that are not empty with faint dots.
func joinFacts(facts ...string) string {
	var out []string
	for _, f := range facts {
		if f != "" {
			out = append(out, f)
		}
	}
	return strings.Join(out, sFaint.Render(dot))
}

// hostDetail is the line under the hosts: how it is reached, and what it is.
func (m *Model) hostDetail(h *hostConn, cw int) string {
	parts := []string{"ssh " + h.host.SSH}
	if i := h.info; i != nil {
		for _, s := range []string{i.Distro, plural(i.CPUs, "CPU", "CPUs")} {
			if s != "" && !strings.HasPrefix(s, "0 ") {
				parts = append(parts, s)
			}
		}
		names := make([]string, 0, len(i.Agents))
		for k := range i.Agents {
			names = append(names, k)
		}
		sort.Strings(names)
		if len(names) > 0 {
			parts = append(parts, strings.Join(names, ", "))
		}
	}
	return sFaint.Render(trunc(strings.Join(parts, dot), cw))
}
