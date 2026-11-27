package tui

import (
	"github.com/iamlakshya/simstim/internal/client"
)

// Host health is display metadata from this connection generation's doctor.
// It never supplies a New candidate, settings reader or admission result.
func hostHealthSummaryV1(h *hostConn) (text string, blocked bool) {
	if h == nil || h.info == nil {
		return "", false
	}
	count := 0
	for _, item := range h.info.Health {
		if item.ID == "drain" && item.Severity == "blocks_new" {
			return "draining", true
		}
		if item.Severity == "blocks_new" {
			blocked = true
		}
		if item.Severity == "warn" || item.Severity == "blocks_new" {
			count++
			if text == "" {
				text = client.CleanDetail(item.Message)
			}
		}
	}
	if count > 1 {
		return plural(count, "health item", "health items"), blocked
	}
	return text, blocked
}

func sessionRuntimeWarningsV1(session client.Session) []string {
	health := client.RuntimeHealth(session)
	var warnings []string
	if health.Outdated {
		warnings = append(warnings, "outdated runtime")
	}
	if health.Exposed {
		warnings = append(warnings, "exposed runtime")
	}
	if len(warnings) > 0 {
		warnings = append(warnings, health.Hint)
	}
	return warnings
}
