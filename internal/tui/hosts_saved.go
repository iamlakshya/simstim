package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// hostReloadedMsg is the saved entry of a host, read again: nil when no
// host of the name is saved.
type hostReloadedMsg struct {
	name      string
	host      *client.Host
	tier      client.Tier
	idChanged bool
	tierUnset bool
	err       error
}

// reloadHost reads the saved entry of host name again, in the background.
func (m *Model) reloadHost(name string) tea.Cmd {
	return m.reloadHostFrom(m.backgroundContext(), name)
}

func (m *Model) reloadHostFrom(ctx context.Context, name string) tea.Cmd {
	be := m.be
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		hosts, err := be.LoadHosts(ctx)
		if err != nil {
			return hostReloadedMsg{name: name, err: err}
		}
		tiers, err := be.LoadTiers(ctx, hosts)
		if err != nil {
			return hostReloadedMsg{name: name, err: err}
		}
		msg := hostReloadedMsg{name: name, tier: tiers.Of(name), idChanged: tiers.IDChanged(name), tierUnset: tiers.NeedsAnswer(name)}
		for i := range hosts {
			if hosts[i].Name == name {
				msg.host = &hosts[i]
			}
		}
		return msg
	})
}

// onHostReloaded puts the saved entry of a host on the screen: a host that
// another command removed leaves the list, and one that it changed shows
// its new entry. A host whose route changed is dialled again.
func (m *Model) onHostReloaded(msg hostReloadedMsg) tea.Cmd {
	h := m.hostByName(msg.name)
	switch {
	case msg.err != nil:
		return m.toastErr(msg.err)
	case msg.host == nil:
		return m.removeHost(msg.name)
	case h != nil && sameRoute(h.host, *msg.host):
		h.host, h.tier, h.idChanged = *msg.host, msg.tier, msg.idChanged
		h.tierUnset = msg.tierUnset
		return nil
	}
	return m.addTierHost(*msg.host, msg.tier, msg.idChanged, nil, nil)
}

// sameRoute reports whether a and b reach one host the same way.
func sameRoute(a, b client.Host) bool {
	return a.SSH == b.SSH && a.SSHConfig == b.SSHConfig && client.SameMachine(a.Machine, b.Machine) &&
		(a.Provider == nil) == (b.Provider == nil) && (a.Provider == nil || *a.Provider == *b.Provider)
}

// removeHost forgets h here. Its sessions keep running on the host.
func (m *Model) removeHost(name string) tea.Cmd {
	for i, h := range m.hosts {
		if h.name() != name {
			continue
		}
		if h.dialCancel != nil {
			h.dialCancel()
		}
		m.hosts = append(m.hosts[:i], m.hosts[i+1:]...)
		h.gen++
		if m.cur == name {
			m.screen, m.cur = scrHosts, ""
		}
		m.syncSel()
		if h.conn != nil {
			return m.dropConn(h.conn)
		}
		return nil
	}
	return nil
}
