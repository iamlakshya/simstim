package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func (w *wizard) view(m *Model) string {
	width := m.dialogWidth()
	cw := width - 4
	title := "Add a host"
	var body []string
	var hints []hint
	switch w.step {
	case wzWhere:
		body, hints = w.whereView(cw)
	case wzTest:
		body = append([]string{"", sBold.Render("Testing " + w.host.Name), ""}, w.checkLines(m, cw)...)
		hints = []hint{{"enter", "next"}, {"esc", "back"}}
		switch {
		case w.ask != nil:
			body = append(body, w.askView(cw)...)
			hints = []hint{{"enter", "pin this key"}, {"esc", "back"}}
		case w.err != nil:
			hints = []hint{{"enter", "try again"}, {"esc", "back"}}
		}
	case wzTier:
		body, hints = w.tierView(cw)
	case wzInstall:
		title = fmt.Sprintf("Install %s on %s?", brand.HostBinary, w.host.Name)
		body = w.installView(m, cw)
		hints = []hint{{"enter", "install"}, {"esc", "back"}}
	case wzReady:
		body = w.readyView(m, cw)
		hints = []hint{{"enter", "start a session"}, {"esc", "close"}}
		switch {
		case !w.saved && w.err != nil:
			hints = []hint{{"enter", "try again"}, {"esc", "back"}}
		case !w.saved:
			hints = []hint{{"esc", "back"}}
		case w.checking:
			hints = []hint{{"esc", "close"}}
		case w.wantsAgents():
			hints = []hint{{"enter", "choose agents"}, {"esc", "close"}}
		}
	case wzAgents:
		title = "Agents on " + w.host.Name
		body, hints = w.agentsView(m, cw)
	case wzRun:
		title, body, hints = w.runView(m, cw)
	case wzDone:
		title = w.host.Name + " is set up"
		body, hints = w.doneView(cw)
	}
	if w.guided && title == "Add a host" {
		title = "Set up"
	}
	var tail []string
	if w.err != nil {
		tail = errorLines(w.err, cw)
	}
	return sheet(width, title, w.stepLabel(), hints, fitBody(body, tail, m.height), toneNormal)
}

// stepLabel is the step count at the right of the sheet's rule.
func (w *wizard) stepLabel() string {
	n := int(w.step) + 1
	switch {
	case w.step == wzDone:
		return sFaint.Render("done")
	case w.step == wzRun && w.act.Kind == onboard.ActLogin:
		n = wizSteps
	case w.step == wzRun:
		n = int(wzAgents) + 1
	}
	return sFaint.Render(fmt.Sprintf("step %d of %d", n, wizSteps))
}

func (w *wizard) whereView(cw int) ([]string, []hint) {
	const lw = 13
	label := func(i int, s string) string {
		if w.field == i {
			return sBold.Render(fit(s, lw))
		}
		return sFaint.Render(fit(s, lw))
	}
	fw := cw - lw
	body := []string{
		"",
		sBold.Render("Where does it live?"),
		"",
		label(0, "SSH target") + w.in[0].view(fw, w.field == 0),
	}
	if list := w.matches(); len(list) > 0 {
		shown := strings.Join(list, ", ")
		count := fmt.Sprintf("  (%d found)", len(list))
		body = append(body, strings.Repeat(" ", lw)+sFaint.Render(trunc("From your SSH config: "+shown, fw-cells(count))+count))
	} else if len(w.known) > 0 {
		body = append(body, strings.Repeat(" ", lw)+sFaint.Render(trunc("Nothing in your SSH config matches.", fw)))
	}
	body = append(body,
		label(1, "Name")+w.in[1].view(fw, w.field == 1),
		label(2, "SSH config")+w.in[2].view(fw, w.field == 2),
	)
	hints := []hint{{"↑↓", "pick from SSH config"}, {"tab", "next field"}, {"enter", "test"}, {"esc", "cancel"}}
	if w.field != 0 {
		hints[0] = hint{"↑↓", "field"}
	}
	return body, hints
}

var tierChoices = [...]struct {
	title string
	note  string
	tier  client.Tier
}{
	{"Only me", "own: gets your secrets when you ask", client.TierOwn},
	{"Someone else", "shared: no secret unless you mark it", client.TierShared},
	{"A machine for one task", "throwaway: short-lived secrets only", client.TierThrowaway},
}

func (w *wizard) tierKey(m *Model, ks string) tea.Cmd {
	switch ks {
	case "up", "k":
		w.tierPick = max(w.tierPick-1, 0)
	case "down", "j":
		w.tierPick = min(w.tierPick+1, len(tierChoices)-1)
	case "enter":
		return w.pickTier(m)
	case "1", "2", "3":
		w.tierPick = int(ks[0] - '1')
		return w.pickTier(m)
	}
	return nil
}

func (w *wizard) pickTier(m *Model) tea.Cmd {
	if w.tierPick < 0 || w.tierPick >= len(tierChoices) {
		return nil
	}
	w.tier = tierChoices[w.tierPick].tier
	if w.plat.Installed {
		return w.setup(m)
	}
	w.step, w.err = wzInstall, nil
	return nil
}

func (w *wizard) tierView(cw int) ([]string, []hint) {
	return tierQuestionView(w.host.Name, w.tierPick, cw)
}

func tierQuestionView(name string, pick, cw int) ([]string, []hint) {
	body := make([]string, 0, 3+len(tierChoices))
	body = append(body, "", sBold.Render("Who else has root on "+name+"?"), "")
	titleW := min(24, max(cw-30, 22))
	for i, c := range tierChoices {
		row := marker(i == pick) + listRow(i == pick,
			seg{fmt.Sprintf("%d ", i+1), sFaint},
			seg{fit(c.title, titleW), sBold},
			seg{"  " + trunc(c.note, max(cw-titleW-6, 0)), sFaint})
		body = append(body, row)
	}
	return body, []hint{{"↑↓", "pick"}, {"enter", "next"}, {"esc", "back"}}
}

func (w *wizard) checkLines(m *Model, cw int) []string { return m.checkLines(w.checks, cw) }

// askView asks about a key that the user's settings do not name, as ssh
// asks at a terminal, with the command that shows the key on the machine.
func (w *wizard) askView(cw int) []string {
	body := []string{""}
	body = append(body, wrap(fmt.Sprintf("%s shows the host key %s, which your known_hosts does not name. Check it on the machine first:",
		w.host.Name, w.ask.Fingerprint), cw)...)
	body = append(body, "", "  "+sKey.Render(trunc(w.ask.CheckCommand(), cw-2)), "")
	return append(body, wrap("Pin it only if they match. Every connection then checks it.", cw)...)
}

// checkLines draws the progress of a step: a spinner, then ✓ or ✕, the
// check's name, and its detail.
func (m *Model) checkLines(checks []check, cw int) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		var mark string
		switch c.state {
		case ckRunning:
			mark = sKey.Render(m.spinner())
		case ckOK:
			mark = sSuccess.Render(glyphOK)
		case ckFailed:
			mark = sDanger.Render(glyphFail)
		default:
			mark = sFaint.Render(glyphIdle)
		}
		detail := c.detail
		if c.took >= time.Millisecond {
			detail = strings.TrimSpace(detail + " " + fmtLatency(c.took))
		}
		out = append(out, mark+" "+fit(c.name, 12)+sFaint.Render(trunc(detail, cw-14)))
	}
	return out
}

func (w *wizard) installView(m *Model, cw int) []string {
	ver := m.be.HelperVersion()
	size := ""
	if n, ok := m.be.HelperSize(w.plat); ok {
		size = fmt.Sprintf(", about %.0f MB", float64(n)/1e6)
	}
	field := func(k, v string) string { return "  " + sFaint.Render(fit(k, 8)) + trunc(v, cw-10) }
	body := []string{""}
	body = append(body, wrap(brand.DisplayName+" needs a small helper on the host to keep sessions alive.", cw)...)
	body = append(body, "",
		field("What", brand.HostBinary+" "+ver+size+", SHA-256 checked"),
		field("Where", fmt.Sprintf("~/.local/share/%s/bin/%s/", brand.Name, ver)),
		field("Also", "turn on linger, install this terminal's terminfo"),
		"")
	body = append(body, wrap("Nothing listens on the network, and nothing runs until you start a session.", cw)...)
	if w.run || len(w.checks) > 0 && w.checks[0].state == ckFailed {
		body = append(append(body, ""), w.checkLines(m, cw)...)
	}
	return body
}

func (w *wizard) readyView(m *Model, cw int) []string {
	body := append([]string{""}, w.checkLines(m, cw)...)
	if !w.saved || w.info == nil {
		return body
	}
	i := w.info
	var names []string
	for k := range i.Agents {
		names = append(names, k)
	}
	sort.Strings(names)
	field := func(k, v string) string { return "  " + sFaint.Render(fit(k, 8)) + trunc(v, cw-10) }
	sys := []string{}
	for _, s := range []string{i.Distro, i.Arch, plural(i.CPUs, "CPU", "CPUs"), fmtSize(i.MemTotal)} {
		if s != "" && !strings.HasPrefix(s, "0 ") {
			sys = append(sys, s)
		}
	}
	sysLine := strings.Join(sys, dot)
	if w.tier == client.TierShared {
		sysLine += "  reported by " + w.host.Name
	}
	return append(body,
		"",
		sSuccess.Render(glyphOK)+" "+sBold.Render(w.host.Name+" is ready."),
		"",
		field("Agents", strings.Join(names, dot)),
		field("System", sysLine),
		field("Limits", fmtSize(i.MemoryMax)+" for sessions"),
	)
}
