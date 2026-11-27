package cli

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

func showPendingPolicy(s client.Session) string {
	s = client.CleanSession(s)
	if len(s.PolicyNextStart) == 0 {
		return ""
	}
	keys := append([]string{}, s.PolicyNextStart...)
	for at, key := range keys {
		if key == "sandbox.wall" && s.PolicyNextWall != "" {
			keys[at] = "sandbox.wall=" + s.PolicyNextWall
		}
	}
	return strings.Join(keys, ", ") + " (at the next start)"
}

func showSessionWall(s client.Session) string {
	if s.Wall == nil {
		return ""
	}
	s = client.CleanSession(s)
	if s.Wall == nil || s.Wall.Level == "" {
		return "unknown"
	}
	value := s.Wall.Level
	if sessionAccountMarkV1(s) != "" {
		value += " (own account)"
	}
	if s.Wall.DowngradedFrom != "" {
		value += ", below the floor " + s.Wall.DowngradedFrom
	}
	return value
}
