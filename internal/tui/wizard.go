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
	wzTier
	wzInstall
	wzReady
	wzAgents // the checklist
	wzRun    // one install or login, in a session the user watches
	wzDone   // the summary, and the first session
)

const wizSteps = 7

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

	host     client.Host
	tier     client.Tier
	tierPick int
	plat     client.Platform
	checks   []check
	err      error
	run      bool
	seq      int
	ctx      context.Context
	cancel   context.CancelFunc
	tok      *wizOp
	staged   bool // this wizard successfully opened the stage of host
	pinScope *wizardPin
	conn     Conn
	info     *client.HostInfo
	saved    bool

	// The host's pin, as host add makes it: staged before the first
	// connection, and saved with the host. ask is a key that the user's
	// settings do not name, which the wizard shows and asks about. want is
	// the fingerprint of the key that the user said yes to.
	pin  *client.Pin
	ask  *client.Pin
	want string
	// saving is the save of the host with its pin while it runs: Esc waits
	// for it, and so does the launcher's end, before the stage goes.
	saving *pendingSave
	was    *client.Host

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
		owner  *wizard
		seq    int
		p      client.Platform
		err    error
		took   time.Duration
		pin    *client.Pin // the pin staged before the probe
		pinErr error       // the pin failed, and nothing connected
	}
	installMsg struct {
		owner *wizard
		seq   int
		err   error
		took  time.Duration
	}
	setupMsg struct {
		owner *wizard
		seq   int
		conn  Conn
		res   client.SetupResult
		info  client.HostInfo
		err   error
		took  time.Duration
	}
)

func (m *Model) openWizard() tea.Cmd {
	m.modal = newWizard()
	return m.loadSSHHosts()
}

func newWizard() *wizard {
	w := &wizard{pick: -1, tierPick: -1}
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

// beforeStagePin is a test hook right before probe opens a stage.
var beforeStagePin = func() {}

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
			if w.ask != nil {
				// The user's yes to the key that the wizard shows.
				w.want, w.ask = w.ask.Fingerprint, nil
				return w.probe(m)
			}
			if w.err != nil {
				return w.probe(m)
			}
			w.step, w.err = wzTier, nil
		}
	case wzTier:
		return w.tierKey(m, ks)
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
// closes before it is ready: as it is saved now, which another command
// may have changed or removed.
func (w *wizard) back(m *Model) tea.Cmd {
	if w.saved && w.step >= wzAgents {
		return w.agentBack(m)
	}
	if w.saving != nil && w.saving.hasBegun() && (w.staged || w.pinScope != nil) {
		return nil // a begun save retains its staged pin until it finishes
	}
	inStage := w.stop()
	var cleanup tea.Cmd
	if !w.saved && !w.made && w.step != wzTier && w.step != wzInstall {
		// The wizard closes or starts again: the pin it staged goes. When
		// StagePin is still waiting, wizOp.stage cleans up after it wakes.
		if w.pinScope != nil {
			cleanup = w.retireOwnedPin(m)
		} else if !inStage && w.staged && w.host.Name != "" {
			m.be.AbortPin(w.host.Name)
		}
		w.staged = false
		w.pin, w.ask, w.want = nil, nil, ""
	}
	drop := m.dropConn(w.takeConn())
	switch {
	case w.made && !w.saved:
		m.modal = nil
		return tea.Batch(drop, cleanup, m.reloadHost(w.host.Name))
	case w.step == wzWhere && w.prev != nil:
		m.modal = w.prev
	case w.step == wzWhere || w.saved:
		m.modal = nil
	case w.step == wzInstall && !w.made:
		w.step = wzTier
	case w.step == wzInstall, w.step == wzTier:
		w.step = wzTest
	default:
		w.step, w.err, w.checks = wzWhere, nil, nil
	}
	return tea.Batch(drop, cleanup)
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
		w.err = &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", name), Hint: "Use letters, digits, dots, dashes and underscores, with no dot at the end.", Code: "bad_name", Kind: client.KindUsage}
	case m.hostByName(name) != nil:
		w.err = &client.UserError{Msg: "there is already a host named " + name, Hint: "Pick another name, or remove " + name + " first.", Code: "host_exists"}
	}
	if w.err != nil {
		return nil
	}
	w.host = client.Host{Name: name, SSH: target, SSHConfig: strings.TrimSpace(w.in[2].value())}
	w.pin, w.ask, w.want = nil, nil, ""
	w.was = nil
	w.tier, w.tierPick = client.TierUnknown, -1
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

// probe pins the host's key, as host add does, then connects and reads
// the host's system. A host that the guided setup made has its pin
// already. Every connection after the pin checks it: the probe, the
// install and the setup.
func (w *wizard) probe(m *Model) tea.Cmd {
	w.step, w.err, w.ask = wzTest, nil, nil
	w.checks = []check{{name: "connect", state: ckRunning}}
	pinFirst := !w.made && w.pin == nil
	if pinFirst {
		w.checks = []check{{name: "host key", state: ckRunning}}
	}
	ctx, seq, cleanup := w.begin(m)
	var tok *wizOp
	if pinFirst {
		tok = &wizOp{prior: w.staged, priorOwned: w.pinScope}
		w.tok = tok
		cancel := w.cancel
		m.conns.onClose(func() {
			cancel()
			tok.finish()
		})
	}
	be, h, want, clock := m.be, w.host, w.want, m.now
	return tea.Batch(cleanup, func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		start := clock()
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var pin *client.Pin
		if pinFirst {
			beforeStagePin()
			bound, ok, err := tok.stage(cctx, be, h.Name)
			if err != nil {
				return probeMsg{owner: w, seq: seq, pinErr: err}
			}
			if !ok {
				return probeMsg{owner: w, seq: seq, pinErr: context.Canceled}
			}
			cctx = bound
			p, err := be.PinHostKey(cctx, h, want)
			if err != nil {
				return probeMsg{owner: w, seq: seq, pinErr: err}
			}
			pin = &p
		}
		p, err := be.Probe(cctx, h)
		return probeMsg{owner: w, seq: seq, p: p, err: err, took: clock().Sub(start), pin: pin}
	})
}

func (w *wizard) install(m *Model) tea.Cmd {
	w.step, w.err = wzInstall, nil
	w.checks = []check{{name: "install", state: ckRunning, detail: "uploading " + brand.HostBinary}}
	ctx, seq, cleanup := w.begin(m)
	be, h, p, clock := m.be, w.host, w.plat, m.now
	return tea.Batch(cleanup, m.bgCmd(func(context.Context) tea.Msg {
		start := clock()
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		err := be.Install(cctx, h, p)
		return installMsg{owner: w, seq: seq, err: err, took: clock().Sub(start)}
	}))
}

// setup connects, prepares the host, reads its facts and saves it.
func (w *wizard) setup(m *Model) tea.Cmd {
	w.step, w.err = wzReady, nil
	w.checks = []check{{name: "prepare", state: ckRunning, detail: "linger, terminfo"}}
	ctx, seq, cleanup := w.begin(m)
	be, h, set, clock := m.be, w.host, m.conns, m.now
	idle := w.made && h.Machine != nil && onboard.PowersOffWhenIdle(h.Machine.Provider)
	return tea.Batch(cleanup, m.bgCmd(func(context.Context) tea.Msg {
		start := clock()
		cctx, cancel := wizardSetupContext(ctx)
		defer cancel()
		c, err := dialClean(cctx, be, h)
		if err != nil {
			return setupMsg{owner: w, seq: seq, err: err}
		}
		if cctx.Err() != nil {
			c.Close()
			return setupMsg{owner: w, seq: seq, err: cctx.Err()}
		}
		if !set.addUnadopted(c) {
			return nil
		}
		res, err := c.Setup(cctx)
		if err != nil {
			set.drop(c)
			return setupMsg{owner: w, seq: seq, err: err}
		}
		info, err := c.Doctor(cctx)
		if err != nil {
			set.drop(c)
			return setupMsg{owner: w, seq: seq, err: err}
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
		if cctx.Err() != nil {
			set.drop(c)
			return setupMsg{owner: w, seq: seq, err: cctx.Err()}
		}
		return setupMsg{owner: w, seq: seq, conn: c, res: res, info: info, took: clock().Sub(start)}
	}))
}
