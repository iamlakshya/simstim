package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type newHostAction uint8

const (
	newHostQuick newHostAction = iota
	newHostForm
	newHostChoice
	newHostSubmit
	newHostLegacyQuick
)

type newHostLookup struct {
	seq    uint64
	ctx    context.Context
	cancel context.CancelFunc
	form   *newSession
	line   *promptState
	action newHostAction
}

type newHostTierResolvedMsg struct {
	seq        uint64
	resolution hostTierResolvedMsg
}

type newHostResolvedMsg struct {
	seq      uint64
	selector string
	host     client.Host
	err      error
	action   newHostAction
	form     *newSession
	args     client.NewArgs
	attach   bool
	modal    modal
	prompt   *promptState
	screen   screen
	current  string
}

// resolveNewHost is only local UI selection. It cannot reserve a session,
// fetch a value, wake a machine or replace the original accepted connection.
func (m *Model) resolveNewHost(selector string, action newHostAction, form *newSession, args client.NewArgs, attach bool) tea.Cmd {
	m.cancelNewHostLookup()
	ctx, cancel := context.WithCancel(client.WithoutWake(m.backgroundContext()))
	m.newHost.ctx, m.newHost.cancel, m.newHost.form, m.newHost.action = ctx, cancel, form, action
	seq, be := m.newHost.seq, m.be
	message := newHostResolvedMsg{seq: seq, selector: selector, action: action, form: form, args: args, attach: attach,
		modal: m.modal, prompt: m.prompt, screen: m.screen, current: m.cur}
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		message.host, message.err = be.ResolveHost(ctx, selector)
		return message
	})
}

func (m *Model) cancelNewHostLookup() {
	if m.newHost.cancel != nil {
		m.newHost.cancel()
		if line := m.newHost.line; line != nil && m.newHost.action == newHostSubmit {
			line.starting = false
		}
		if form := m.newHost.form; form != nil {
			form.choosingHost = false
			if m.newHost.action == newHostSubmit {
				form.starting = false
			}
		}
	}
	m.newHost.ctx, m.newHost.cancel, m.newHost.form, m.newHost.line = nil, nil, nil, nil
	m.newHost.seq++
}

// A submit is still waiting on local selection: keep the normal form rule
// that typing while it starts changes nothing. Escape may cancel it.
func (m *Model) newHostLookupKey(key string) bool {
	if m.newHost.cancel == nil {
		return false
	}
	if m.newHost.ctx == nil || m.newHost.ctx.Err() != nil {
		m.cancelNewHostLookup()
		return false
	}
	if _, asking := m.modal.(*hostTierQuestion); asking && key != "esc" && key != "ctrl+c" {
		return false
	}
	if m.newHost.action == newHostSubmit && key != "esc" && key != "ctrl+c" {
		return true
	}
	m.cancelNewHostLookup()
	return false
}

func (m *Model) onNewHostResolved(msg newHostResolvedMsg) tea.Cmd {
	if msg.seq != m.newHost.seq {
		return nil
	}
	owned := m.newHost.ctx != nil && m.newHost.ctx.Err() == nil && m.modal == msg.modal && m.prompt == msg.prompt && m.screen == msg.screen && m.cur == msg.current
	m.cancelNewHostLookup()
	if !owned || errors.Is(msg.err, context.Canceled) {
		return nil
	}
	if msg.err != nil {
		return m.newHostLookupFailure(msg)
	}
	host := m.hostByName(msg.host.Name)
	if host == nil || !client.SameEntry(host.host, msg.host) {
		err := &client.UserError{Code: "host_changed", Kind: client.KindRefused,
			Msg: "the saved host changed while the launcher selected it", Hint: "Refresh the host and choose it again."}
		return tea.Batch(m.newHostSelectionError(msg.form, err), m.reloadHost(msg.host.Name))
	}
	switch msg.action {
	case newHostLegacyQuick:
		return m.openQuickStartOn(host)
	case newHostQuick, newHostForm:
		return m.openDefaultNewSession(host, msg)
	case newHostChoice:
		if m.modal == msg.form {
			msg.form.useHost(host, msg.host)
		}
		return nil
	case newHostSubmit:
		return m.submitResolvedNewHost(host, msg)
	}
	return nil
}

func (m *Model) newHostSelectionError(form *newSession, err error) tea.Cmd {
	if form != nil && m.modal == form {
		form.starting, form.choosingHost, form.err = false, false, err
		return nil
	}
	return m.toastErr(err)
}

func (m *Model) withNewHostTier(host *hostConn, form *newSession, next func() tea.Cmd) tea.Cmd {
	m.cancelNewHostLookup()
	ctx, cancel := context.WithCancel(client.WithoutWake(m.backgroundContext()))
	m.newHost.ctx, m.newHost.cancel, m.newHost.form = ctx, cancel, form
	m.newHost.action = newHostForm
	if form != nil {
		m.newHost.action = newHostSubmit
		form.starting = true
	}
	seq := m.newHost.seq
	command := m.withHostTierContext(ctx, host, next)
	return func() tea.Msg {
		message := command()
		if result, ok := message.(hostTierResolvedMsg); ok {
			return newHostTierResolvedMsg{seq: seq, resolution: result}
		}
		return message
	}
}

func (m *Model) onNewHostTierResolved(msg newHostTierResolvedMsg) tea.Cmd {
	if msg.seq != m.newHost.seq {
		return nil
	}
	live := m.newHost.ctx != nil && m.newHost.ctx.Err() == nil
	m.cancelNewHostLookup()
	if !live {
		return nil
	}
	return m.onHostTierResolved(msg.resolution)
}

func (m *Model) newHostLookupFailure(msg newHostResolvedMsg) tea.Cmd {
	var user *client.UserError
	if msg.selector == "" && (msg.action == newHostQuick || msg.action == newHostForm) && errors.As(msg.err, &user) && user.Code == "host_required" {
		if msg.action == newHostQuick {
			if host := m.newSessionHost(); host != nil {
				return m.resolveNewHost(host.name(), newHostLegacyQuick, nil, client.NewArgs{}, false)
			}
			return m.openQuickStartSelected()
		}
		return m.openNewSessionSelected()
	}
	return m.newHostSelectionError(msg.form, msg.err)
}

func (m *Model) openDefaultNewSession(host *hostConn, msg newHostResolvedMsg) tea.Cmd {
	return m.withNewHostTier(host, nil, func() tea.Cmd {
		if m.modal != msg.modal || !client.SameEntry(host.host, msg.host) {
			return nil
		}
		if err := client.Allow(host.host, host.tier, client.Outbound{Kind: client.OutboundDefaultHost}, client.DefaultSettings()); err != nil {
			return m.toastErr(err)
		}
		dialog := newSessionDialog(m, host)
		dialog.selector, dialog.sourceHost, dialog.defaultHost = "", msg.host, host.name()
		m.modal = dialog
		return nil
	})
}

func (m *Model) submitResolvedNewHost(host *hostConn, msg newHostResolvedMsg) tea.Cmd {
	if msg.form == nil || m.modal != msg.form || msg.form.host != host.name() || !client.SameEntry(msg.form.sourceHost, msg.host) {
		return m.newHostSelectionError(msg.form, &client.UserError{Code: "host_changed", Kind: client.KindRefused,
			Msg: "the form's host selection changed before the start", Hint: "Close this form, then choose the host again."})
	}
	msg.form.starting = true
	return m.withNewHostTier(host, msg.form, func() tea.Cmd {
		if m.modal != msg.form || !client.SameEntry(host.host, msg.host) {
			return nil
		}
		if msg.selector == "" {
			if err := client.Allow(host.host, host.tier, client.Outbound{Kind: client.OutboundDefaultHost}, client.DefaultSettings()); err != nil {
				return m.newHostSelectionError(msg.form, err)
			}
		}
		msg.form.starting = true
		return m.startSessionReady(host, startReq{args: msg.args, attach: msg.attach, form: msg.form})
	})
}
