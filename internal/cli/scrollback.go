package cli

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// scrollbackAdvice is what host doctor says about sessions that draw full
// screen: their scrollback lives inside the agent, not in your terminal.
// When the agent has an inline mode, it says how to start it that way.
func scrollbackAdvice(sessions []client.Session) []string {
	var out []string
	for _, s := range sessions {
		if !s.Alive || !s.FullScreen {
			continue
		}
		a, ok := agents.Find(s.Agent)
		title := s.Agent
		if ok {
			title = a.Title
		}
		if ok && a.Inline.Has() {
			out = append(out, fmt.Sprintf("%s (%s) draws full screen, so your terminal keeps no scrollback of it. Try: start it with `%s new … --inline`, or set agents.inline = true in config.toml", s.Name, title, brand.Name))
		} else {
			out = append(out, fmt.Sprintf("%s (%s) draws full screen and keeps its own scrollback; it has no inline mode", s.Name, title))
		}
	}
	return out
}

// printScrollback prints the advice for a host's sessions, if any.
func printScrollback(ctx context.Context, conn *client.Conn) {
	sessions, err := conn.Sessions(ctx)
	if err != nil {
		return
	}
	for _, line := range scrollbackAdvice(client.CleanSessions(sessions)) {
		fmt.Printf("  ! scrollback: %s\n", line)
	}
}
