package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type rentKeyError struct {
	*client.UserError
	cause error
}

func (e rentKeyError) Unwrap() []error { return []error{e.UserError, e.cause} }

func (g *guide) onRentKey(msg rentKeyMsg) tea.Cmd {
	if !g.owns(msg.scope) || g.step != gdRentKey || g.run {
		return nil
	}
	g.seq++
	g.plan.Key, g.err = msg.key, nil
	if msg.err != nil && msg.key == "" {
		g.err = rentKeyError{
			UserError: &client.UserError{
				Msg:  "could not make the SSH key: " + msg.err.Error(),
				Hint: onboard.KeyCommand,
				Code: "ssh_key_failed",
			},
			cause: msg.err,
		}
	}
	return nil
}
