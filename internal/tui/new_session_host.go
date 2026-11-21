package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func (d *newSession) useHost(host *hostConn, selected client.Host) {
	d.stopFolders()
	currentAgent := d.agent()
	d.host, d.sourceHost, d.selector = host.name(), selected, host.name()
	d.preview = hostSelectionPreview{}
	d.choices, d.finding, d.ai = agentChoices(host.info), host.info == nil, 0
	for index, choice := range d.choices {
		if choice.installed && choice.kind == currentAgent {
			d.ai = index
			break
		}
	}
	d.suggest, d.si, d.hints, d.choosingHost = nil, 0, false, false
	d.downgrade, d.err = false, nil
}

func (d *newSession) hostRow(m *Model, width int) string {
	text := d.selectionName()
	if d.selector == "" && d.preview.defaulted && (d.preview.automatic || d.host == d.defaultHost) {
		text += " (default)"
	}
	if d.choosingHost {
		text += "…"
	}
	if d.field == 6 && (len(m.hosts) > 1 || d.preview.facadeValidated && len(m.hosts) > 0) {
		text = "‹ " + text + " ›"
	}
	return trunc(text, width)
}

func (m *Model) chooseNewSessionHost(d *newSession, key string) tea.Cmd {
	step := 0
	switch key {
	case "left", "h":
		step = -1
	case "right", "l", "space":
		step = 1
	}
	if step == 0 || len(m.hosts) == 0 {
		return nil
	}
	// Auto is explicit local intent. Its actual target comes only from the
	// real attempt owner after the user submits the form.
	names := []string{}
	if d.preview.facadeValidated {
		names = append(names, "auto")
	}
	index := 0
	for _, host := range m.hosts {
		if client.ValidHostName(host.name()) {
			names = append(names, host.name())
			if host.name() == d.host {
				index = len(names) - 1
			}
		}
	}
	if len(names) < 2 {
		if key == "space" && len(names) == 1 {
			d.choosingHost, d.err = true, nil
			return m.resolveNewHost(names[0], newHostChoice, d, client.NewArgs{}, false)
		}
		return nil
	}
	name := names[(index+step+len(names))%len(names)]
	d.choosingHost, d.err = true, nil
	return m.resolveNewHost(name, newHostChoice, d, client.NewArgs{}, false)
	return nil
}

func (m *Model) openQuickStartSelected() tea.Cmd {
	if m.prompt != nil {
		m.prompt.stopFolders()
	}
	host := m.newSessionHost()
	if host == nil {
		m.backToHosts()
		return m.toast(toastInfo, "Choose a host, then press n.")
	}
	return m.openQuickStartOn(host)
}

func (m *Model) openQuickStartOn(host *hostConn) tea.Cmd {
	if !host.online() {
		return m.toastErr(offlineErr(host))
	}
	if host.idChanged || host.conn.HostIDChanged() {
		return m.toastHostIDChanged(host.name())
	}
	m.prompt = &promptState{mode: promptNew, host: host.name(), in: newInput("", m.defaultAgent(host)+" ~/folder name"), at: m.now()}
	return nil
}

func (m *Model) openNewSessionSelected() tea.Cmd {
	if host := m.newSessionHost(); host != nil {
		owner, selected := m.modal, host.host
		return m.withNewHostTier(host, nil, func() tea.Cmd {
			if m.modal != owner || !client.SameEntry(host.host, selected) {
				return nil
			}
			m.modal = newSessionDialog(m, host)
			return nil
		})
	}
	m.backToHosts()
	return m.toast(toastInfo, "Choose a host, then press n.")
}

func (m *Model) submitNewSessionHost(d *newSession, args client.NewArgs, attach bool) tea.Cmd {
	d.stopFolders()
	d.starting, d.err = true, nil
	// The default is resolved again, rather than silently adopting its new
	// host. Explicit choices still have to be saved as the form showed them.
	return m.resolveNewHost(d.selector, newHostSubmit, d, args, attach)
}
