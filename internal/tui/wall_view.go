package tui

import (
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// wallMark takes one cell, including for an unwalled session. The mark is
// drawn by the launcher only; an attached session keeps the whole terminal.
func wallMark(s client.Session) seg {
	if s.Wall == nil {
		return seg{" ", sFaint}
	}
	if s.Wall.DowngradedFrom != "" {
		return seg{"!", sWarning}
	}
	mark := " "
	switch s.Wall.Level {
	case "microvm":
		mark = "▣"
	case "gvisor":
		mark = "▤"
	case "process":
		mark = "□"
	}
	return seg{mark, sFaint}
}

func wallLevel(value string) string {
	switch value {
	case "none", "process", "gvisor", "microvm":
		return value
	default:
		return "unknown"
	}
}

func sessionWallLevel(s client.Session) string {
	if s.Wall == nil {
		return ""
	}
	level := wallLevel(s.Wall.Level)
	if level == "process" {
		return "process (shares your account)"
	}
	return level
}

// sessionWallWarnings lists names only. No secret value enters the launcher.
// The values remain raw in the model for requests and JSON; this copy is text.
func sessionWallWarnings(s client.Session) []string {
	if s.Wall == nil {
		return nil
	}
	var out []string
	if s.Wall.DowngradedFrom != "" {
		out = append(out, "! "+wallLevel(s.Wall.Level)+", below the floor "+wallLevel(s.Wall.DowngradedFrom))
	}
	if s.Wall.Level == "none" && s.Wall.DowngradedFrom == "" {
		return out
	}
	presentation := client.PresentSessionSecrets(s)
	shownName := func(name string) string {
		if text := shown(name); text != "" {
			return text
		}
		return "[hidden name]"
	}
	for _, name := range presentation.Exposed {
		out = append(out, "exposed: "+shownName(name))
	}
	for _, name := range presentation.Unknown {
		out = append(out, "secret delivery unknown: "+shownName(name))
	}
	if len(presentation.Unarmed) > 0 {
		out = append(out, "secrets not armed: attach to arm them")
	}
	return out
}

// wallDetail keeps a warning visible even when the activity and folder would
// otherwise use the whole line. More detail remains in the policy sheet.
func wallDetail(s client.Session, facts []string, cw int) string {
	warnings := sessionWallWarnings(s)
	if len(warnings) == 0 {
		return ""
	}
	if s.Wall.DowngradedFrom == "" {
		warnings = append(warnings, sessionWallLevel(s))
	}
	notice := strings.Join(warnings, dot)
	text := sWarning.Render(trunc(notice, cw))
	facts = slices.DeleteFunc(slices.Clone(facts), func(s string) bool { return s == "" })
	facts = shownAll(facts)
	if rest := strings.Join(facts, dot); rest != "" && cells(notice+dot+rest) <= cw {
		text += sFaint.Render(dot + rest)
	}
	return text
}

func hostWallFloor(h *hostConn) string {
	if h.host.WallFloor == "" {
		return "not pinned"
	}
	return wallLevel(h.host.WallFloor)
}

// hostWallNow uses the full capability report. An old helper or a report
// without runtime evidence leaves the current level unknown, never guessed.
func hostWallNow(h *hostConn) string {
	if h.info == nil || h.info.Sandbox == nil {
		return "unknown"
	}
	now, _ := api.LevelsFor(*h.info.Sandbox, api.RuntimeStatus{})
	return wallLevel(now)
}

func hostWallFacts(h *hostConn) string {
	return "floor " + hostWallFloor(h) + dot + "now " + hostWallNow(h)
}
