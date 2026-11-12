package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// onMsg takes the results of the wizard's commands.
func (w *wizard) onMsg(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case probeMsg:
		return w.onProbe(m, msg)
	case installMsg:
		return w.onInstall(m, msg)
	case setupMsg:
		return w.onSetupDone(m, msg)
	case agentChecksMsg:
		return w.onAgentChecks(m, msg)
	case actStartedMsg:
		return w.onActStarted(m, msg)
	case hostSavedMsg:
		return w.onSaved(m, msg)
	}
	return nil
}

// onProbe shows what the probe found, and goes on to the install or the setup.
func (w *wizard) onProbe(m *Model, msg probeMsg) tea.Cmd {
	if msg.seq != w.seq {
		return nil
	}
	w.run, w.cancel = false, nil
	if msg.err != nil {
		w.checks[0].state = ckFailed
		w.err = w.sshErr(msg.err)
		return nil
	}
	w.plat = msg.p
	w.checks = []check{
		{name: "connect", state: ckOK, took: msg.took},
		{name: "system", state: ckOK, detail: client.CleanLine(msg.p.OS + "/" + msg.p.Arch)},
	}
	if _, ok := m.be.HelperSize(msg.p); !ok && !msg.p.Installed {
		w.checks[1].state = ckFailed
		w.err = &client.UserError{
			Msg:  "this build has no helper for " + client.CleanLine(msg.p.OS+"/"+msg.p.Arch),
			Hint: "Use a Linux host on amd64 or arm64.",
			Code: "no_helper",
		}
		return nil
	}
	if msg.p.Installed {
		w.checks = append(w.checks, check{name: "helper", state: ckOK, detail: m.be.HelperVersion() + " found"})
		if w.made {
			return w.setup(m)
		}
	} else {
		w.checks = append(w.checks, check{name: "helper", state: ckNote, detail: "not installed yet"})
		if w.made {
			// Making the host was the user's yes: the consent screen
			// shows what is installed, and the install runs at once.
			w.step = wzInstall
			return w.install(m)
		}
	}
	return nil
}

// onInstall goes on to the setup once the helper is installed.
func (w *wizard) onInstall(m *Model, msg installMsg) tea.Cmd {
	if msg.seq != w.seq {
		return nil
	}
	w.run, w.cancel = false, nil
	if msg.err != nil {
		w.checks[0].state = ckFailed
		w.err = w.sshErr(msg.err)
		return nil
	}
	w.plat.Installed = true
	return w.setup(m)
}

// onSetupDone shows the setup's steps and saves the host.
func (w *wizard) onSetupDone(m *Model, msg setupMsg) tea.Cmd {
	if msg.seq != w.seq {
		if msg.conn != nil {
			return m.dropConn(msg.conn)
		}
		return nil
	}
	if msg.err != nil {
		w.run, w.cancel = false, nil
		w.checks[0].state = ckFailed
		w.err = w.sshErr(msg.err)
		return nil
	}
	w.conn = msg.conn
	info := msg.info
	w.info = &info
	w.checks = []check{{name: "connect", state: ckOK, took: msg.took}}
	for _, s := range msg.res.Steps {
		c := check{name: s.Name, state: ckOK, detail: s.Detail}
		if !s.OK {
			c.state = ckFailed
			if s.Fix != "" {
				c.detail = strings.TrimSpace(c.detail + "  Try: " + s.Fix)
			}
		}
		w.checks = append(w.checks, c)
	}
	return m.saveHosts(w.host, false)
}

// onSaved adds the saved host to the launcher, then checks its agents.
func (w *wizard) onSaved(m *Model, msg hostSavedMsg) tea.Cmd {
	w.run, w.cancel = false, nil
	if msg.err != nil {
		w.err = msg.err
		return nil
	}
	w.saved = true
	conn := w.conn
	w.conn = nil
	cmd := m.addHost(w.host, conn, w.info)
	if h := m.hostByName(w.host.Name); h != nil {
		m.openHost(h)
	}
	return tea.Batch(cmd, w.loadAgents(m))
}
