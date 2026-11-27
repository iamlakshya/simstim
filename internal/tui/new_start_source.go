package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type newStartSourceMsg struct {
	seq          uint64
	host         client.Host
	gen          int
	connection   Conn
	request      startReq
	modal        modal
	prompt       *promptState
	sourceErr    error
	selectionErr error
	refreshed    bool
}

type newStartDialledMsg struct {
	dialled dialledMsg
	host    client.Host
	request startReq
	modal   modal
	prompt  *promptState
}

// This boundary precedes NewR, selected values, answers and reservations.
// An existing logical request always keeps its original connection/failure.
func (m *Model) prepareNewStartSource(host *hostConn, request startReq) tea.Cmd {
	return m.checkNewStartSource(host, request, false)
}

func (m *Model) checkNewStartSource(host *hostConn, request startReq, refreshed bool) tea.Cmd {
	m.cancelNewHostLookup()
	ctx, cancel := context.WithCancel(client.WithoutWake(m.backgroundContext()))
	m.newHost.ctx, m.newHost.cancel, m.newHost.form, m.newHost.line, m.newHost.action = ctx, cancel, request.form, request.line, newHostSubmit
	message := newStartSourceMsg{seq: m.newHost.seq, host: host.host, gen: host.gen, connection: host.conn,
		request: request, modal: m.modal, prompt: m.prompt, refreshed: refreshed}
	selector, backend := host.name(), m.be
	if request.form != nil {
		selector = request.form.selector
	}
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		message.sourceErr = message.connection.CheckSavedSource(ctx)
		if message.sourceErr == nil {
			var selected client.Host
			selected, message.selectionErr = backend.ResolveHost(ctx, selector)
			if message.selectionErr == nil && !client.SameEntry(selected, message.host) {
				message.selectionErr = &client.UserError{Code: "host_changed", Kind: client.KindRefused,
					Msg: "the host selection changed before the new start", Hint: "Choose the host again."}
			}
			if message.selectionErr == nil {
				message.sourceErr = message.connection.CheckSavedSource(ctx)
			}
		}
		return message
	})
}

func (m *Model) onNewStartSource(msg newStartSourceMsg) tea.Cmd {
	if msg.seq != m.newHost.seq {
		return nil
	}
	live := m.newHost.ctx != nil && m.newHost.ctx.Err() == nil
	m.cancelNewHostLookup()
	if !live || m.modal != msg.modal || m.prompt != msg.prompt {
		return nil
	}
	host := m.hostByName(msg.host.Name)
	if host == nil || host.gen != msg.gen || host.conn != msg.connection || !host.online() || !client.SameEntry(host.host, msg.host) {
		return m.newStartSourceError(msg.request, &client.UserError{Code: "host_changed", Kind: client.KindRefused,
			Msg: "the selected host changed before the new start", Hint: "Choose the host again."})
	}
	if msg.selectionErr != nil {
		return m.newStartSourceError(msg.request, msg.selectionErr)
	}
	if errors.Is(msg.sourceErr, context.Canceled) || errors.Is(msg.sourceErr, context.DeadlineExceeded) || msg.refreshed && msg.sourceErr != nil {
		return m.newStartSourceError(msg.request, msg.sourceErr)
	}
	if msg.sourceErr == nil {
		m.markNewStartWaiting(msg.request)
		return m.startSessionWithSource(host, msg.request)
	}
	// The canonical default writer may invalidate Source while leaving the
	// entry's visible fields unchanged. Reuse the existing owned reconnect
	// and adoption path now, before any logical value request exists.
	m.markNewStartWaiting(msg.request)
	command := m.reconnect(host)
	return func() tea.Msg {
		message := command()
		if result, ok := message.(dialledMsg); ok {
			return newStartDialledMsg{dialled: result, host: msg.host, request: msg.request, modal: msg.modal, prompt: msg.prompt}
		}
		return message
	}
}

func (m *Model) onNewStartDialled(msg newStartDialledMsg) tea.Cmd {
	// A completed reconnect may remain the launcher's shared connection even
	// after the form leaves. No new value request or start follows that form.
	adoption := m.onDialled(msg.dialled)
	if m.modal != msg.modal || m.prompt != msg.prompt {
		return adoption
	}
	host := m.hostByName(msg.host.Name)
	if msg.dialled.err != nil {
		return tea.Batch(adoption, m.newStartSourceError(msg.request, msg.dialled.err))
	}
	if host == nil || host.gen != msg.dialled.gen || host.conn != msg.dialled.conn || !host.online() || !client.SameEntry(host.host, msg.host) || host.idChanged || host.conn.HostIDChanged() {
		return tea.Batch(adoption, m.newStartSourceError(msg.request, &client.UserError{Code: "host_changed", Kind: client.KindRefused,
			Msg: "the host identity changed before the new start", Hint: "Check the host identity, then choose the host again."}))
	}
	m.markNewStartWaiting(msg.request)
	return tea.Batch(adoption, m.checkNewStartSource(host, msg.request, true))
}

func (m *Model) markNewStartWaiting(request startReq) {
	if request.form != nil && m.modal == request.form {
		request.form.starting = true
	}
	if request.line != nil && m.prompt == request.line {
		request.line.starting = true
	}
}

func (m *Model) newStartSourceError(request startReq, err error) tea.Cmd {
	if request.form != nil && m.modal == request.form {
		request.form.starting, request.form.err = false, err
		return nil
	}
	if request.line != nil && m.prompt == request.line {
		request.line.starting, request.line.err = false, err
		return nil
	}
	return m.toastErr(err)
}
