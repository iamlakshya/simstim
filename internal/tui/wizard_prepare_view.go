package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func buildSandboxChecks(probe api.CapProbe, rt api.RuntimeStatus, took time.Duration) []check {
	nowLevel, afterRoot := api.LevelsFor(probe, rt)
	probeDetail := shown(nowLevel) + " now"
	if afterRoot != "" && afterRoot != "none" && afterRoot != nowLevel {
		probeDetail += " · " + shown(afterRoot) + " with root"
	}
	rtDetail := "pinned images ready"
	if rt.Runtime != "" {
		rtDetail = shown(rt.Runtime) + " ready"
	}
	checks := []check{
		{name: "probe", state: ckOK, detail: probeDetail, took: took},
		{name: "runtime", state: ckOK, detail: rtDetail},
	}
	for _, wall := range probe.Walls {
		st, detail := ckNote, shown(wall.Reason)
		if wall.OK {
			st, detail = ckOK, shown(wall.Level)
			if wall.Version != "" {
				detail = strings.TrimSpace(detail + " · " + shown(wall.Version))
			}
		} else if detail == "" {
			detail = "unavailable"
		}
		checks = append(checks, check{name: shown(wall.Driver), state: st, detail: detail})
	}
	return checks
}

func upsertSandboxCheck(list []check, c check) []check {
	out := append([]check(nil), list...)
	for i := range out {
		if out[i].name == c.name {
			out[i] = c
			return out
		}
	}
	return append(out, c)
}

func (w *wizard) sandboxesView(m *Model, cw int) ([]string, []hint) {
	body := append([]string{""}, m.checkLines(w.sandboxChecks, cw)...)
	switch {
	case w.run:
		return body, []hint{{"esc", "close"}}
	case w.err != nil:
		return body, []hint{{"enter", "try again"}, {"s", "continue"}, {"esc", "close"}}
	case w.rootPreview.Needed:
		_, afterRoot := api.LevelsFor(w.capProbe, w.rtStatus)
		line := "Optional root setup is available on " + shown(w.host.Name) + "."
		if afterRoot != "" && afterRoot != "none" {
			line = fmt.Sprintf("Optional root setup unlocks %s on %s.", shown(afterRoot), shown(w.host.Name))
		}
		body = append(append(body, ""), wrap(line, cw)...)
		return body, []hint{{"enter", "root step"}, {"s", "skip"}, {"esc", "close"}}
	default:
		return body, []hint{{"esc", "close"}}
	}
}

func (w *wizard) rootView(m *Model, cw int) ([]string, []hint) {
	body := []string{""}
	intro := "Review what runs with sudo on " + shown(w.host.Name) + ":"
	if w.rootPreview.Tier == client.TierShared {
		intro = shown(w.host.Name) + " is a shared host. Print the script for its owner, or confirm the host name to run it:"
	}
	body = append(body, wrap(intro, cw)...)
	body = append(body, "")
	for _, st := range w.rootPreview.Script.Steps {
		body = append(body, "• "+trunc(shown(st.Reason), max(cw-2, 0)))
		body = append(body, "  "+sAccent.Render(trunc(shown(st.Line), max(cw-2, 0))))
	}
	body = append(body, "", sFaint.Render("SHA-256  ")+trunc(shown(w.rootPreview.Script.SHA256), max(cw-9, 0)))
	if w.run || w.waitingRoot {
		body = append(append(body, ""), m.checkLines([]check{{name: "root step", state: ckRunning, detail: "preparing"}}, cw)...)
		return body, []hint{{"esc", "back"}}
	}
	if w.rootPreview.DefaultMode == client.RootRun {
		return body, []hint{{"enter", "run with sudo"}, {"p", "print"}, {"s", "skip"}, {"esc", "back"}}
	}
	return body, []hint{{"enter", "print script"}, {"r", "run"}, {"s", "skip"}, {"esc", "back"}}
}
