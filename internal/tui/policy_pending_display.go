package tui

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

func pendingPolicyLabel(s client.Session) string {
	s = client.CleanSession(s)
	if len(s.PolicyNextStart) == 0 {
		return ""
	}
	keys := append([]string{}, s.PolicyNextStart...)
	for at, key := range keys {
		if key == "sandbox.wall" && s.PolicyNextWall != "" {
			keys[at] = "wall=" + s.PolicyNextWall
		}
	}
	return "at next start: " + strings.Join(keys, ", ")
}
