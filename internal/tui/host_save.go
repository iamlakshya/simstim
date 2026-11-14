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
	warn       error
	completion *hostSaveCompletion
}

func (m *Model) applyHostSaved(ctx context.Context, msg hostSavedMsg) tea.Cmd {
	if hostChanged(msg.err) {
		text, _ := errText(msg.err)
		return tea.Batch(m.toastNext(toastError, sentence(text), "The list now shows the host as it is saved. Try again if you still want to."), m.reloadHostFrom(ctx, msg.host.Name))
	}
	if msg.err != nil {
		return m.toastErr(msg.err)
	}
	if msg.remove {
		cmds := []tea.Cmd{m.removeHost(msg.host.Name), m.toast(toastSuccess, fmt.Sprintf("Forgot %s. Its sessions and the helper stay on the host.", shown(msg.host.Name)))}
		if msg.warn != nil {
			text, hint := errText(msg.warn)
			cmds = append(cmds, m.toastNext(toastWarning, sentence(text), hint))
		}
		return tea.Batch(cmds...)
	}
	return nil
}

func (m *Model) saveWizardHost(w *wizard, wctx context.Context, seq int, host client.Host) tea.Cmd {
	be, pending := m.be, w.saving
	was, tier, hostID := w.was, w.tier, ""
	if w.conn != nil {
		hostID = w.conn.HostID()
	}
	return m.bgCmdFrom(wctx, func(ctx context.Context) tea.Msg {
		msg := hostSavedMsg{owner: w, seq: seq, host: host}
		if err := ctx.Err(); err != nil {
			msg.err = err
			return msg
		}
		if pending != nil {
			if !pending.begin() {
				msg.err = context.Canceled
				return msg
			}
			defer close(pending.done)
		}
		msg.warn, msg.err = saveWizardHost(ctx, be, client.HostChange{Kind: "the launcher", Was: was, Now: &host, Pin: true, Tier: tier, HostID: hostID})
		if pending != nil {
			pending.err = msg.err
		}
		return msg
	})
}

// saveHosts is the legacy package-local call shape, routed through CAS.
func (m *Model) saveHosts(host client.Host, remove bool) tea.Cmd {
	var was *client.Host
	if h := m.hostByName(host.Name); h != nil {
		copy := h.host
		was = &copy
	}
	c := client.HostChange{Was: was, Now: &host}
	if remove {
		if c.Was == nil {
			c.Was = &host
		}
		c.Now = nil
	}
	return m.changeHost(c)
}
