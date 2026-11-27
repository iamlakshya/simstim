package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
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
	case sandboxInspectMsg:
		return w.onSandboxInspect(m, msg)
	case rootExecDoneMsg:
		return w.onRootExecDone(m, msg)
	case rootActionDoneMsg:
		return w.onRootActionDone(m, msg)
	case sandboxBootMsg:
		return w.onSandboxBoot(m, msg)
	}
	return nil
}

// onProbe shows what the probe found, and goes on to the install or the setup.
func (w *wizard) onProbe(m *Model, msg probeMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || w.step != wzTest || !w.run {
		return nil
	}
	if w.tok != nil {
		w.tok.mu.Lock()
		w.staged = w.staged || w.tok.staged
		if w.tok.staged {
			w.pinScope = w.tok.scope
		}
		w.tok.mu.Unlock()
		w.tok = nil
	}
	w.finishOp()
	w.run = false
	if msg.pinErr != nil {
		return w.onPinFailed(msg.pinErr)
	}
	if msg.pin != nil {
		w.pin = msg.pin
	}
	w.checks = nil
	if w.pin != nil {
		w.checks = []check{{name: "host key", state: ckOK, detail: strings.TrimPrefix(w.pin.Line(), "host key ")}}
	}
	if msg.err != nil {
		w.checks = append(w.checks, check{name: "connect", state: ckFailed})
		w.err = w.sshErr(msg.err)
		return nil
	}
	w.plat = msg.p
	w.checks = append(w.checks,
		check{name: "connect", state: ckOK, took: msg.took},
		check{name: "system", state: ckOK, detail: client.CleanLine(msg.p.OS + "/" + msg.p.Arch)},
	)
	sys := len(w.checks) - 1
	if _, ok := m.be.HelperSize(msg.p); !ok && !msg.p.Installed {
		w.checks[sys].state = ckFailed
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
			w.prepareAfterSave = canPrepareSandbox(m.be)
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

// onPinFailed shows why the host's key was not pinned. A key that the
// user's settings do not name is shown, with a question: the launcher owns
// the terminal, so ssh cannot ask. Nothing connected to the host.
func (w *wizard) onPinFailed(err error) tea.Cmd {
	var uk *client.UncheckedKeyError
	if errors.As(err, &uk) {
		p := uk.Pin
		w.ask = &p
		w.checks = []check{{name: "host key", state: ckNote, detail: "not in your known_hosts"}}
		return nil
	}
	w.checks = []check{{name: "host key", state: ckFailed}}
	w.err = err
	return nil
}

// onInstall goes on to the setup once the helper is installed.
func (w *wizard) onInstall(m *Model, msg installMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || w.step != wzInstall || !w.run {
		return nil
	}
	w.run = false
	w.finishOp()
	if msg.err != nil {
		w.checks[0].state = ckFailed
		w.err = w.sshErr(msg.err)
		return nil
	}
	w.plat.Installed = true
	w.prepareAfterSave = canPrepareSandbox(m.be)
	return w.setup(m)
}

// onSetupDone shows the setup's steps and saves the host.
func (w *wizard) onSetupDone(m *Model, msg setupMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || w.step != wzReady || !w.run || w.saved {
		if msg.conn != nil {
			return m.dropConn(msg.conn)
		}
		return nil
	}
	if msg.err != nil {
		w.run = false
		w.finishOp()
		w.checks[0].state = ckFailed
		w.err = w.sshErr(msg.err)
		return nil
	}
	w.conn = msg.conn
	info := msg.info
	w.info = &info
	w.checks = []check{{name: "connect", state: ckOK, took: msg.took}}
	// The host sends the steps, and the helper the idle stop's line: each
	// is cleaned before it reaches the screen.
	for _, s := range msg.res.Steps {
		c := check{name: api.CleanText(s.Name, errTextMax), state: ckOK, detail: api.CleanText(s.Detail, errTextMax)}
		if !s.OK {
			c.state = ckFailed
			if s.Fix != "" {
				c.detail = strings.TrimSpace(c.detail + "  Try: " + api.CleanText(s.Fix, errTextMax))
			}
		}
		w.checks = append(w.checks, c)
	}
	return w.save(m)
}

// onSaved adds the saved host to the launcher, then checks its agents.
func (w *wizard) onSaved(m *Model, msg hostSavedMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || w.step != wzReady || !w.run || w.saved {
		return nil
	}
	w.run = false
	w.finishOp()
	w.saving = nil
	if msg.err != nil {
		w.err = msg.err
		cmds := []tea.Cmd{m.dropConn(w.takeConn())}
		if hostChanged(msg.err) {
			cmds = append(cmds, m.reloadHost(w.host.Name))
		}
		return tea.Batch(cmds...)
	}
	w.saved = true
	w.pinScope, w.staged = nil, false
	conn := w.takeConn()
	cmd := m.addTierHost(w.host, w.tier, false, conn, w.info)
	if h := m.hostByName(w.host.Name); h != nil {
		m.openHost(h)
	}
	if w.prepareAfterSave && msg.target != nil {
		if sb, ok := m.be.(sandboxPrepareBackend); ok {
			w.prepTarget = msg.target
			return tea.Batch(cmd, w.startSandboxPrepare(m, sb))
		}
	}
	return tea.Batch(cmd, w.loadAgents(m))
}
