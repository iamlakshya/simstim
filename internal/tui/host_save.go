package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type hostSavedMsg struct {
	owner      *wizard
	seq        int
	host       client.Host
	remove     bool
	err        error
	completion *hostSaveCompletion
}

func (m *Model) applyHostSaved(msg hostSavedMsg) tea.Cmd {
	if msg.err != nil {
		return m.toastErr(msg.err)
	}
	if msg.remove {
		return tea.Batch(m.removeHost(msg.host.Name),
			m.toast(toastSuccess, fmt.Sprintf("Forgot %s. Its sessions and the helper stay on the host.", msg.host.Name)))
	}
	return nil
}

func changedHosts(hosts []client.Host, host client.Host, remove bool) []client.Host {
	out := make([]client.Host, 0, len(hosts)+1)
	for _, h := range hosts {
		if h.Name != host.Name {
			out = append(out, h)
		}
	}
	if !remove {
		out = append(out, host)
	}
	return out
}

func (m *Model) saveWizardHost(w *wizard, wctx context.Context, seq int, host client.Host) tea.Cmd {
	be := m.be
	return m.bgCmdFrom(wctx, func(ctx context.Context) tea.Msg {
		msg := hostSavedMsg{owner: w, seq: seq, host: host}
		if err := ctx.Err(); err != nil {
			msg.err = err
			return msg
		}
		hosts, err := be.LoadHosts(ctx)
		if err != nil {
			msg.err = err
			return msg
		}
		if err := ctx.Err(); err != nil {
			msg.err = err
			return msg
		}
		msg.err = be.SaveHosts(ctx, changedHosts(hosts, host, false))
		return msg
	})
}
