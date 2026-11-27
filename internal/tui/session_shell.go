package tui

import (
	"context"
	"errors"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// SessionShellRunner runs the one CLI shell implementation with the actual
// accepted parent connection and exact selected ID. It prepares a scoped
// command before handoff; the returned runner receives restored terminal files.
// The command tree injects it without a CLI/TUI import cycle.
type SessionShellRunner func(context.Context, Conn, string, string) (func(*os.File, *os.File) error, func() error, error)

func (m *Model) openSessionShell() tea.Cmd {
	if m.transferInputBlocked() {
		return nil
	}
	host, session := m.selectedPair()
	if host == nil || session == nil || m.sessionShell == nil {
		return nil
	}
	if !host.online() {
		return m.toastErr(offlineErr(host))
	}
	if !client.ValidHostName(host.name()) || !api.ValidSessionID(session.ID) {
		return m.toastErr(&client.UserError{Msg: "the selected shell target has no exact host and session ID",
			Hint: "Choose the session again.", Code: "bad_address", Kind: client.KindRefused})
	}
	name, id, parent, ctx, prepare := host.name(), session.ID, host.conn, m.ctx, m.sessionShell
	return m.withHostTier(host, func() tea.Cmd {
		if host.name() != name || host.conn != parent {
			return m.toastErr(&client.UserError{Msg: "the selected shell host changed", Hint: "Choose the session again.", Code: "host_changed", Kind: client.KindRefused})
		}
		run, closeScope, err := prepare(ctx, parent, name, id)
		if closeScope == nil {
			closeScope = func() error { return nil }
		}
		if err != nil {
			return m.toastErr(errors.Join(err, closeScope()))
		}
		if run == nil {
			return m.toastErr(errors.Join(&client.UserError{Msg: "the session shell has no original command", Hint: "Choose the session again.", Code: "shell_unavailable", Kind: client.KindFailed}, closeScope()))
		}
		m.conns.onClose(func() { _ = closeScope() })
		x := &funcExec{run: func(in, out *os.File) (returned error) {
			defer func() { returned = errors.Join(returned, closeScope()) }()
			if err := ctx.Err(); err != nil {
				return err
			}
			return run(in, out)
		}}
		return m.execWithInput(x, func(err error) tea.Msg { return shellDoneMsg{host: name, err: err} })
	})
}

func (m *Model) runPolicyLockdown() tea.Cmd {
	if m.transferInputBlocked() || m.policyLockdown == nil {
		return nil
	}
	ctx, run := m.ctx, m.policyLockdown
	x := &funcExec{run: func(in, out *os.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return run(ctx, in, out)
	}}
	return m.execWithInput(x, func(err error) tea.Msg { return opDoneMsg{ok: "Lockdown enabled.", err: err} })
}
