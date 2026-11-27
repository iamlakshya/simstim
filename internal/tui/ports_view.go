package tui

import (
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

func forwardSummary(forwards []api.PortForwardInfo) string {
	var labels []string
	for _, forward := range forwards {
		if forward.Port < 1 || forward.Port > 65535 || len(labels) >= 64 {
			continue
		}
		label := "port "
		if forward.Reverse {
			label = "reverse "
		}
		labels = append(labels, label+strconv.Itoa(forward.Port))
	}
	return strings.Join(labels, ", ")
}
