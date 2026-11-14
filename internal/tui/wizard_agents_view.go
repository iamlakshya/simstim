package tui

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

const agentW = 18 // the width of an agent's title in the lists

// agentsView is the checklist: the agents the host has are ticked, and the
// best-effort ones wait under a fold.
func (w *wizard) agentsView(m *Model, cw int) ([]string, []hint) {
	body := []string{""}
	if w.flow == nil {
		return append(body, m.checkLines([]check{{name: "agents", state: ckRunning, detail: "asking the host"}}, cw)...), []hint{{"esc", "back"}}
	}
	body = append(body, wrap("Tick the agents to install on "+w.host.Name+". Each one installs with its vendor's own installer, in a session you watch.", cw)...)
	body = append(body, "")
	rows := w.rows()
	more := 0
	for _, it := range w.flow.Items {
		if it.BestEffort() {
			more++
		}
	}
	shownHead := false
	for r, i := range rows {
		sel := r == w.row
		if i < 0 {
			body = append(body, marker(sel)+listRow(sel, seg{"▸ " + plural(more, "more agent", "more agents") + ", best effort", sFaint}))
			continue
		}
		it := w.flow.Items[i]
		if it.BestEffort() && !shownHead {
			shownHead = true
			body = append(body, "  "+sFaint.Render("Best effort: kept working by contributors"))
		}
		glyph := sFaint.Render(glyphIdle)
		switch {
		case it.Installed():
			glyph = sSuccess.Render(glyphOK)
		case it.Chosen:
			glyph = sAccent.Render(glyphOnline)
		}
		body = append(body, marker(sel)+listRow(sel, seg{glyph + " ", sPlain}, seg{fit(it.Agent.Title, agentW), sBold}, seg{trunc(it.State(), max(cw-agentW-4, 0)), sFaint}))
	}
	next := hint{"enter", "next"}
	if n := len(w.flow.Chosen()); n > 0 {
		next = hint{"enter", "install " + plural(n, "agent", "agents")}
	}
	return body, []hint{{"space", "tick"}, next, {"esc", "back"}}
}

// runView is one install or login, before and after its session.
func (w *wizard) runView(m *Model, cw int) (string, []string, []hint) {
	a := w.act
	body := []string{""}
	if l := w.last; l != nil {
		body = append(body, lineRow(*l, cw)...)
		body = append(body, "")
	}
	var title, cmd, text string
	switch a.Kind {
	case onboard.ActLogin:
		title, cmd = "Log in to "+a.Agent.Title+"?", a.Agent.Setup.Login
		text = fmt.Sprintf("%s is installed, but not logged in. It logs in its own way, in a session named %s on %s. %s never sees the login.",
			a.Agent.Title, a.Args.Name, w.host.Name, brand.DisplayName)
	default:
		title, cmd = "Install "+a.Agent.Title+"?", a.Agent.Setup.Install
		text = fmt.Sprintf("%s (%d of %d) installs with its vendor's installer, from %s. It runs in a session named %s on %s, where you can answer its questions.",
			a.Agent.Title, a.N, a.Of, a.Agent.Setup.Source, a.Args.Name, w.host.Name)
	}
	body = append(body, wrap(text, cw)...)
	body = append(body, "", "  "+sAccent.Render(trunc(cmd, cw-2)), "")
	for _, l := range wrap(api.CleanText(client.LoadSettings().Attach.DetachKey, 64)+" leaves the session running, and comes back here.", cw) {
		body = append(body, sFaint.Render(l))
	}
	hints := []hint{{"enter", "run it"}, {"s", "skip"}, {"esc", "stop"}}
	if a.Kind == onboard.ActLogin {
		hints[0] = hint{"enter", "log in now"}
	}
	switch {
	case w.starting:
		body = append(append(body, ""), m.checkLines([]check{{name: "start", state: ckRunning, detail: a.Args.Name}}, cw)...)
		hints = nil
	case w.checking:
		body = append(append(body, ""), m.checkLines([]check{{name: "check", state: ckRunning, detail: "asking " + w.host.Name + " about " + a.Agent.Title}}, cw)...)
		hints = nil
	}
	return title, body, hints
}

// doneView sums up each agent, and gives the first session.
func (w *wizard) doneView(cw int) ([]string, []hint) {
	body := []string{""}
	if w.flow == nil {
		return body, []hint{{"esc", "close"}}
	}
	for _, it := range w.flow.Shown() {
		body = append(body, lineRow(w.flow.Line(it.Agent.Kind), cw)...)
	}
	body = append(body, "", sBold.Render("Start your first session:"), "  "+sAccent.Render(trunc(w.flow.FirstSession(), cw-2)))
	return body, []hint{{"enter", "start a session"}, {"esc", "close"}}
}

// lineRow draws one agent of a summary: its mark in colour, its title, a
// short state, and the step to try under it.
func lineRow(l onboard.Line, cw int) []string {
	var mark string
	switch l.Mark {
	case "✓":
		mark = sSuccess.Render(glyphOK)
	case "!":
		mark = sWarning.Render("!")
	case "✕":
		mark = sDanger.Render(glyphFail)
	default:
		mark = sFaint.Render("?")
	}
	out := []string{mark + " " + sBold.Render(fit(l.Title, agentW)) + sFaint.Render(trunc(l.Short, max(cw-agentW-2, 0)))}
	if l.Try != "" && l.Mark != "✓" {
		out = append(out, "  "+sAccent.Render(glyphNext)+" "+sLink.Render(trunc(l.Try, cw-4)))
	}
	return out
}
