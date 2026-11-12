package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// The add-host wizard (docs/contributing/ux-spec.md 2.10): one step per screen, Esc goes back.
// After "ready" come the agent checklist, the installs, the logins and a
// summary (wizard_agents.go).
type wizStep int

const (
	wzWhere wizStep = iota
	wzTest
	wzInstall
	wzReady
	wzAgents // the checklist
	wzRun    // one install or login, in a session the user watches
	wzDone   // the summary, and the first session
)

const wizSteps = 6

type checkState int

const (
	ckRunning checkState = iota
	ckOK
	ckFailed
	ckNote
)

// check is one line of a step's progress: a spinner, then ✓ or ✕.
type check struct {
	name   string
	state  checkState
	detail string
	took   time.Duration
}

type wizard struct {
	step  wizStep
	field int // 0 target, 1 name, 2 SSH config
	in    [3]input
	named bool // the name was typed, so it no longer follows the target
	known []string
	pick  int

	host   client.Host
	plat   client.Platform
	checks []check
	err    error
	run    bool
	seq    int
	cancel context.CancelFunc
	conn   Conn
	info   *client.HostInfo
	saved  bool

	// guided is set when the guided setup opened the wizard: the agent
	// checklist always follows "ready". made is set for a host the setup
	// just made, which counts as the user's yes to the helper. prev is the
	// sheet that Esc on the first step returns to.
	guided bool
	made   bool
	prev   modal

	agentPart
}

// Wizard results.
type (
	probeMsg struct {
		seq  int
		p    client.Platform
		err  error
		took time.Duration
	}
	installMsg struct {
		seq  int
		err  error
		took time.Duration
	}
	setupMsg struct {
		seq  int
		conn Conn
		res  client.SetupResult
		info client.HostInfo
		err  error
		took time.Duration
	}
)

func (m *Model) openWizard() tea.Cmd {
	m.modal = newWizard()
	be := m.be
	return func() tea.Msg { return sshHostsMsg{be.SSHConfigHosts()} }
}

func newWizard() *wizard {
	w := &wizard{pick: -1}
	w.in[0] = newInput("", "a Host from your SSH config, or user@host")
	w.in[1] = newInput("", "the SSH target")
	w.in[2] = newInput("", "optional: an extra SSH config file")
	return w
}

func (w *wizard) busy() bool { return w.run || w.checking }

func (w *wizard) paste(s string) {
	if w.step == wzWhere {
		w.in[w.field].insert(s)
		w.follow()
	}
}

// follow keeps the name in step with the target until it is typed.
func (w *wizard) follow() {
	if w.named {
		return
	}
	t := w.in[0].value()
	if i := strings.LastIndex(t, "@"); i >= 0 {
		t = t[i+1:]
	}
	if i := strings.Index(t, ":"); i >= 0 {
		t = t[:i]
	}
	w.in[1].set(t)
}

// matches are the SSH config hosts that fit what is typed.
func (w *wizard) matches() []string {
	q := w.in[0].value()
	if q == "" || w.pick >= 0 {
		return w.known
	}
	var out []string
	for _, k := range w.known {
		if _, ok := fuzzy(q, k); ok {
			out = append(out, k)
		}
	}
	return out
}

func (w *wizard) stop() {
	w.seq++
	w.run = false
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}

func (w *wizard) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	if ks == "esc" {
		return w.back(m)
	}
	switch w.step {
	case wzWhere:
		return w.whereKey(m, k)
	case wzTest:
		if ks == "enter" && !w.run {
			if w.err != nil {
				return w.probe(m)
			}
			if w.plat.Installed {
				return w.setup(m)
			}
			w.step, w.err = wzInstall, nil
		}
	case wzInstall:
		if ks == "enter" && !w.run {
			return w.install(m)
		}
	case wzReady:
		if ks == "enter" && !w.run && !w.checking {
			if w.err != nil {
				return w.setup(m)
			}
			if w.saved && w.wantsAgents() {
				w.step = wzAgents
				return nil
			}
			if w.saved {
				w.startSession(m, "")
			}
		}
	case wzAgents, wzRun, wzDone:
		return w.agentKey(m, k)
	}
	return nil
}

// back goes one step back, or closes the wizard from the first step. A
// host the setup made is kept, and joins the list, even when the wizard
// closes before it is ready.
func (w *wizard) back(m *Model) tea.Cmd {
	if w.saved && w.step >= wzAgents {
		return w.agentBack(m)
	}
	w.stop()
	switch {
	case w.made && !w.saved:
		m.modal = nil
		return m.addHost(w.host, nil, nil)
	case w.step == wzWhere && w.prev != nil:
		m.modal = w.prev
	case w.step == wzWhere || w.saved:
		m.modal = nil
	case w.step == wzInstall:
		w.step = wzTest
	default:
		w.step, w.err, w.checks = wzWhere, nil, nil
	}
	return nil
}

func (w *wizard) whereKey(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "tab":
		w.field = (w.field + 1) % 3
		return nil
	case "shift+tab":
		w.field = (w.field + 2) % 3
		return nil
	case "up", "down":
		if w.field != 0 {
			step := map[string]int{"up": -1, "down": 1}[k.String()]
			w.field = min(max(w.field+step, 0), 2)
			return nil
		}
		list := w.matches()
		if len(list) == 0 {
			return nil
		}
		if k.String() == "down" {
			w.pick = (w.pick + 1) % len(list)
		} else {
			w.pick = (w.pick - 1 + len(list)) % len(list)
		}
		w.in[0].set(list[w.pick])
		w.follow()
		return nil
	case "enter":
		return w.submit(m)
	}
	if w.in[w.field].key(k) {
		switch w.field {
		case 0:
			w.pick = -1
			w.follow()
		case 1:
			w.named = true
		}
	}
	return nil
}

// submit checks the first step and starts the test.
func (w *wizard) submit(m *Model) tea.Cmd {
	target := strings.TrimSpace(w.in[0].value())
	name := strings.TrimSpace(w.in[1].value())
	w.err = nil
	switch {
	case target == "":
		w.err = &client.UserError{Msg: "an SSH target is needed", Hint: "Type a Host from ~/.ssh/config, or user@host.", Code: "missing_value", Kind: client.KindUsage}
	case !client.ValidHostName(name):
		w.err = &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", name), Hint: "Use letters, digits, dots, dashes and underscores.", Code: "bad_name", Kind: client.KindUsage}
	case m.hostByName(name) != nil:
		w.err = &client.UserError{Msg: "there is already a host named " + name, Hint: "Pick another name, or remove " + name + " first.", Code: "host_exists"}
	}
	if w.err != nil {
		return nil
	}
	w.host = client.Host{Name: name, SSH: target, SSHConfig: strings.TrimSpace(w.in[2].value())}
	return w.probe(m)
}

// cliHint is the command that does the same with prompts allowed.
func (w *wizard) cliHint() string {
	s := fmt.Sprintf("%s host add %s", brand.Name, w.host.Name)
	if w.host.SSH != w.host.Name {
		s += " " + w.host.SSH
	}
	if w.host.SSHConfig != "" {
		s += " --ssh-config " + w.host.SSHConfig
	}
	return s + " (it can ask for passwords)"
}

// sshErr explains a failed SSH step. The wizard cannot show SSH's prompts,
// so the way out is the CLI, which can.
func (w *wizard) sshErr(err error) error {
	msg, hint := errText(err)
	if offline(err) || strings.Contains(msg, "Permission denied") || strings.Contains(msg, "Host key") || hint == "" {
		return &client.UserError{Msg: msg, Hint: w.cliHint(), Code: "ssh_failed"}
	}
	return err
}

func (w *wizard) begin(m *Model) (context.Context, int) {
	w.stop()
	w.run = true
	ctx, cancel := context.WithCancel(m.ctx)
	w.cancel = cancel
	return ctx, w.seq
}

func (w *wizard) probe(m *Model) tea.Cmd {
	w.step, w.err = wzTest, nil
	w.checks = []check{{name: "connect", state: ckRunning}}
	ctx, seq := w.begin(m)
	be, h, clock := m.be, w.host, m.now
	return func() tea.Msg {
		start := clock()
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		p, err := be.Probe(cctx, h)
		return probeMsg{seq: seq, p: p, err: err, took: clock().Sub(start)}
	}
}

func (w *wizard) install(m *Model) tea.Cmd {
	w.err = nil
	w.checks = []check{{name: "install", state: ckRunning, detail: "uploading " + brand.HostBinary}}
	ctx, seq := w.begin(m)
	be, h, p, clock := m.be, w.host, w.plat, m.now
	return func() tea.Msg {
		start := clock()
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		err := be.Install(cctx, h, p)
		return installMsg{seq: seq, err: err, took: clock().Sub(start)}
	}
}

// setup connects, prepares the host, reads its facts and saves it.
func (w *wizard) setup(m *Model) tea.Cmd {
	w.step, w.err = wzReady, nil
	w.checks = []check{{name: "prepare", state: ckRunning, detail: "linger, terminfo"}}
	ctx, seq := w.begin(m)
	be, h, set, clock := m.be, w.host, m.conns, m.now
	idle := w.made && h.Machine != nil && onboard.PowersOffWhenIdle(h.Machine.Provider)
	return func() tea.Msg {
		start := clock()
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		c, err := dialClean(cctx, be, h)
		if err != nil {
			return setupMsg{seq: seq, err: err}
		}
		if !set.add(c) {
			return nil
		}
		res, err := c.Setup(cctx)
		if err != nil {
			set.drop(c)
			return setupMsg{seq: seq, err: err}
		}
		info, err := c.Doctor(cctx)
		if err != nil {
			set.drop(c)
			return setupMsg{seq: seq, err: err}
		}
		if idle {
			st := api.Check{Name: "idle stop", OK: true}
			st.Detail, err = be.Local().SetIdle(cctx, h)
			if err != nil {
				st.OK, st.Detail = false, err.Error()
			}
			st.Detail = client.CleanLine(st.Detail) // the host's own words
			res.Steps = append(res.Steps, st)
		}
		return setupMsg{seq: seq, conn: c, res: res, info: info, took: clock().Sub(start)}
	}
}
