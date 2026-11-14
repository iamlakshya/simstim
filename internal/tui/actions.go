package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// op runs one request on h's connection and reports with a toast.
func (m *Model) op(h *hostConn, ok string, fn func(context.Context, Conn) error) tea.Cmd {
	if !h.online() {
		if m.rename != nil {
			m.rename = nil
		}
		return m.toastErr(offlineErr(h))
	}
	name, c := h.name(), h.conn
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return opDoneMsg{host: name, ok: ok, err: fn(cctx, c)}
	})
}

func offlineErr(h *hostConn) error {
	name := shown(h.name())
	if h.status == stConnecting {
		return &client.UserError{Msg: "still connecting to " + name, Hint: "Wait a moment, then try again.", Code: "connecting"}
	}
	return &client.UserError{Msg: name + " is offline", Hint: fmt.Sprintf("%s host test %s", brand.Name, name), Code: "offline"}
}

// newSessionHost picks the host for "session: new": the host in play, or
// on the list of every host, the selected session's host.
func (m *Model) newSessionHost() *hostConn {
	if h := m.selectedHost(); h != nil {
		return h
	}
	if h, _ := m.selectedPair(); h != nil {
		return h
	}
	if len(m.hosts) == 1 {
		return m.hosts[0]
	}
	return nil
}

func (m *Model) openNewSession() tea.Cmd {
	if h := m.newSessionHost(); h != nil {
		return m.withHostTier(h, m.openNewSessionReady)
	}
	return m.openNewSessionReady()
}

func (m *Model) openNewSessionReady() tea.Cmd {
	h := m.newSessionHost()
	if h == nil {
		m.backToHosts()
		return m.toast(toastInfo, "Choose a host, then press n.")
	}
	m.modal = newSessionDialog(m, h)
	return nil
}

// openSelectedHost is Enter on the hosts list.
func (m *Model) openSelectedHost() tea.Cmd {
	if h := m.selectedHost(); h != nil {
		return m.withHostTier(h, func() tea.Cmd { m.openHost(h); return nil })
	}
	return nil
}

// startSession sends "new" to the host h, for the start r.
func (m *Model) startSession(h *hostConn, r startReq) tea.Cmd {
	return m.withHostTier(h, func() tea.Cmd { return m.startSessionReady(h, r) })
}

func (m *Model) startSessionReady(h *hostConn, r startReq) tea.Cmd {
	r.host = h.name()
	// A name that looks like an ID fails before any secret is fetched.
	if err := client.CheckSessionName(r.args.Name); err != nil {
		return func() tea.Msg { return startedMsg{startReq: r, err: err} }
	}
	if !h.online() {
		err := offlineErr(h)
		return func() tea.Msg { return startedMsg{startReq: r, err: err} }
	}
	c, be, originalHost, kept, cols, rows := h.conn, m.be, h.host, m.settings, m.width, m.height
	read := m.beginSettingsRead()
	return m.bgCmd(func(ctx context.Context) (message tea.Msg) {
		defer func() { message = m.finishStartGrant(message) }()
		settings, read := readSettings(be, kept, read)
		if _, err := client.ReadPolicySettings(); err != nil {
			return startedMsg{startReq: r, err: err, settingsRead: read}
		}
		args, err := client.SessionArgs(r.args, cols, rows, settings)
		if err != nil {
			return startedMsg{startReq: r, err: err, settingsRead: read}
		}
		r.args = args
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if planner, ok := unclean(c).(interface {
			PrepareNewPolicy(context.Context, client.NewArgs) (client.NewArgs, error)
		}); ok {
			r.args, err = planner.PrepareNewPolicy(cctx, r.args)
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if r.grants == nil {
			r.wallIntent = r.args.Wall
		}
		if planner, ok := unclean(c).(interface {
			ResolveNewWall(context.Context, client.NewArgs) (client.NewArgs, error)
		}); ok {
			r.args, err = planner.ResolveNewWall(cctx, r.args)
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		} else if r.args.Wall != "" {
			return startedMsg{startReq: r, err: &client.UserError{Msg: "this host connection cannot select a wall",
				Hint: "Update the client and host helper, then try again.", Code: "wall_helper_old"}, settingsRead: read}
		}
		if r.grants == nil {
			level := r.args.Wall
			if level == "" {
				level = "none"
			}
			r.grants, err = client.NewWallGrantRequest(false, r.args.SSHAgent, level, nil)
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
			m.grantOwners.add(r.grants)
		}
		cctx = client.WithGrantRequest(cctx, r.grants)
		if r.args.Wall == "microvm" && r.vm == nil {
			planner, ok := unclean(c).(interface {
				PrepareNewVM(context.Context, client.NewArgs, string) (*client.VMNeedPlanV1, error)
			})
			if !ok {
				return startedMsg{startReq: r, err: &client.UserError{Msg: "this connection cannot prepare a microvm", Hint: "Update the client and host helper, then try again.", Code: "vm_prepare", Kind: client.KindRefused}, settingsRead: read}
			}
			r.vm, err = planner.PrepareNewVM(cctx, r.args, r.wallIntent)
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if planner, ok := unclean(c).(interface {
			PrepareNewSecrets(context.Context, client.NewArgs, ...*client.VMNeedPlanV1) (client.NewArgs, error)
		}); ok {
			if r.vm != nil {
				r.args, err = planner.PrepareNewSecrets(cctx, r.args, r.vm)
			} else {
				r.args, err = planner.PrepareNewSecrets(cctx, r.args)
			}
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		} else if r.args.Wall != "" && r.args.Wall != "none" {
			return startedMsg{startReq: r, err: &client.UserError{Msg: "this connection cannot prepare sandbox inputs", Hint: "Update the client and host helper, then try again.", Code: "secret_helper_old", Kind: client.KindRefused}, settingsRead: read}
		}
		effectiveNames := client.SecretNames(originalHost, nil)
		if len(r.args.Layers) > 0 {
			effectiveNames, err = client.PolicySecretNames(r.args.Layers)
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if err := m.grantQuestions(cctx, func(ctx context.Context) error {
			return c.CheckNewGrants(ctx, effectiveNames, r.args.SSHAgent)
		}); err != nil {
			return startedMsg{startReq: r, err: err, settingsRead: read}
		}
		if planner, ok := unclean(c).(interface {
			CheckNewPolicyGrants(context.Context, []api.Layer) error
		}); ok {
			if err := m.grantQuestions(cctx, func(ctx context.Context) error {
				return planner.CheckNewPolicyGrants(ctx, r.args.Layers)
			}); err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		var warn *client.UserError
		if r.args.Setup == "" && !r.answered {
			answers := be.SetupAnswers(r.host)
			answers.Apply(&r.args)
			r.answered, r.answers, warn = true, answers, answers.Warning
		}
		// One fetch of the secrets for each start. A question about the
		// start keeps them for the start after the answer.
		if r.args.Secrets == nil {
			var secrets map[string]api.Secret
			err := m.grantQuestions(cctx, func(ctx context.Context) error {
				var err error
				secrets, err = be.Secrets(ctx, settings, originalHost, effectiveNames)
				return err
			})
			if err != nil {
				return startedMsg{startReq: r, err: err, warn: warn, settingsRead: read}
			}
			r.args.Secrets = secrets
		}
		var s *client.Session
		err = m.grantQuestions(cctx, func(ctx context.Context) error {
			var err error
			s, err = c.NewSession(ctx, r.args)
			return err
		})
		return startedMsg{startReq: r, s: s, err: err, warn: warn, settingsRead: read}
	})
}

// onStarted shows how a start went, and a warning about the user's answers
// about setup scripts, when they could not be read.
func (m *Model) onStarted(msg startedMsg) tea.Cmd {
	d, p := m.waiting(msg.startReq)
	waited := d != nil || p != nil
	cmd := m.showStarted(msg)
	if keepStartGrant(msg) && !waited {
		cmd = tea.Batch(cmd, m.releaseGrant(msg.grants))
	}
	if msg.cleanupErr != nil {
		cmd = tea.Batch(cmd, m.toastErr(msg.cleanupErr))
	}
	if w := msg.warn; w != nil {
		return tea.Batch(m.toastSetupWarn(w), cmd)
	}
	return cmd
}

// showStarted shows how a start went, in the form or the quick-start line
// that waits for it, or opens the host's question about it. After esc,
// nothing waits: the launcher asks nothing, and does not attach to the
// session.
func (m *Model) showStarted(msg startedMsg) tea.Cmd {
	d, p := m.waiting(msg.startReq)
	waited := d != nil || p != nil
	if q, ok := downgradeOf(msg.err); ok && waited && msg.grants == nil && msg.args.Wall != "auto" && msg.args.Wall != "" {
		cleanup := m.disposeModal()
		m.modal = &downgradeSheet{request: msg.startReq, level: q.Level, floor: q.Floor, typed: newInput("", q.Level)}
		return cleanup
	}
	if q, ok := client.FolderQuestionOf(msg.err); ok && !msg.args.MakeFolder {
		if waited {
			cleanup := m.disposeModal()
			m.modal = &makeFolderAsk{startReq: msg.startReq, q: q}
			return cleanup
		}
		return nil
	}
	if q, ok := client.SetupQuestionOf(msg.err); ok {
		if waited {
			cleanup := m.disposeModal()
			m.modal = &setupAsk{startReq: msg.startReq, q: q}
			return cleanup
		}
		return nil
	}
	if ue, ok := setupCannotRun(msg.err); ok && msg.args.Setup != api.SetupSkip && waited {
		cleanup := m.disposeModal()
		m.modal = &setupSkipAsk{startReq: msg.startReq, err: ue}
		return cleanup
	}
	if msg.err != nil {
		if d != nil {
			d.starting, d.err = false, msg.err
			return nil
		}
		if p != nil {
			p.starting, p.err = false, msg.err
		}
		return m.toastErr(msg.err)
	}
	if d != nil {
		m.lastAgent[msg.host] = d.agent()
		m.modal = nil
	}
	if p != nil {
		m.prompt = nil
	}
	h := m.hostByName(msg.host)
	if h == nil || msg.s == nil {
		return nil
	}
	h.sessions = append(h.sessions, *msg.s)
	h.changes++
	if waited {
		if m.screen != scrSessions || m.cur != "" && m.cur != h.name() {
			m.openHost(h)
		}
		m.sel, m.autoSel = sessKey{h.name(), msg.s.ID}, false
		m.syncSel()
		if msg.attach {
			return m.jack(h, msg.s.ID, msg.s.Name)
		}
	}
	return tea.Batch(m.toast(toastSuccess, fmt.Sprintf("Started %s on %s.", shown(msg.s.Name), shown(h.name()))), m.listSessions(h))
}

func (m *Model) attachSelected() tea.Cmd {
	if h, s := m.selectedPair(); h != nil && s != nil {
		return m.withHostTierContext(m.backgroundContext(), h, m.attachSelectedReady)
	}
	return m.attachSelectedReady()
}

func (m *Model) attachSelectedReady() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		if sessionRows(m.rows()) == 0 {
			return m.openQuickStart()
		}
		return nil
	}
	if ended(*s) {
		return m.openEnded(h, *s)
	}
	return m.jack(h, s.ID, s.Name)
}

// copyAttach copies the command that attaches to the selected session. It
// names the session by host/ID, never by its name, which can mean another
// session (cli-spec 9.2). An address that a shell would not read back as it
// is, as from a host that sends an odd ID, is not copied.
func (m *Model) copyAttach() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		return nil
	}
	addr := h.name() + "/" + s.ID
	if !api.ValidSessionID(s.ID) || client.ShellWord(addr) != addr {
		return m.toastErr(&client.UserError{Msg: "the address of this session cannot go in a command", Hint: brand.Name + " ls", Code: "bad_address", Kind: client.KindUsage})
	}
	text := brand.Name + " attach --id -- " + addr
	return tea.Batch(tea.SetClipboard(text), m.toast(toastSuccess, "Copied: "+text+"."))
}

func (m *Model) confirmDelete() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		return nil
	}
	m.modal = deleteDialog(h, *s)
	return nil
}

// deleteSession runs after the confirm. s is the session as the question
// showed it: the host deletes it only while its name is still s.Name, so a
// session renamed since stays.
func (m *Model) deleteSession(host string, s client.Session) tea.Cmd {
	h := m.hostByName(host)
	if h == nil {
		return nil
	}
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	id, c := s.ID, h.conn
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		res, err := c.DeleteNamed(cctx, id, s.Name)
		ok := fmt.Sprintf("Deleted %s on %s.", shown(s.Name), shown(host))
		note, hint := client.WorktreeNote(host, res)
		if note != "" {
			ok += " " + sentence(note)
		}
		if hint != "" {
			// An error kept the worktree: a warning, with what to try.
			return opDoneMsg{host: host, warn: ok, hint: hint, err: err, drop: id}
		}
		return opDoneMsg{host: host, ok: ok, err: err, drop: id}
	})
}

// changeHost saves c.Now, or with c.Now nil forgets the host, in the
// background. c.Was is the entry that the launcher read: when another
// command changed the host since, such as a host rm, or a host create of
// the same name, it changes nothing, and fails with host_changed. So a
// removed host never comes back, and a confirmation that waited never
// removes a host made again (cli-spec 11.3, rule 13).
func (m *Model) changeHost(c client.HostChange) tea.Cmd {
	be := m.be
	c.Kind = "the launcher"
	remove := c.Now == nil
	host := c.Now
	if remove {
		host = c.Was
	}
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		// A removed host keeps no record of an unfinished `host rm`. The
		// removal worked already: a failure of that clean-up is a warning.
		warn, err := saveWizardHost(ctx, be, c)
		return m.completeHostSave(hostSavedMsg{host: *host, remove: remove, err: err, warn: warn})
	})
}

// hostChanged reports a save refused because its saved entry changed.
func hostChanged(err error) bool {
	ue, ok := asUserError(err)
	return ok && (ue.Code == "host_changed" || ue.Code == "host_exists")
}

func (m *Model) onHostSaved(msg hostSavedMsg) tea.Cmd {
	if msg.owner == nil || msg.completion != nil {
		var ok bool
		msg, ok = m.hostSaves.acknowledge(msg)
		if !ok {
			return nil
		}
	}
	if msg.owner != nil {
		if w, ok := m.modal.(*wizard); ok && w.owns(msg.owner, msg.seq) && w.step == wzReady && w.run && !w.saved {
			return w.onSaved(m, msg)
		}
		return nil
	}
	return m.applyHostSaved(m.backgroundContext(), msg)
}

// testHost measures the round trip to h.
func (m *Model) testHost(h *hostConn) tea.Cmd {
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	name, c := h.name(), h.conn
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		d, err := c.RoundTrip(cctx)
		if err != nil {
			return opDoneMsg{host: name, err: err}
		}
		return opDoneMsg{host: name, ok: fmt.Sprintf("%s answers in %s.", shown(name), fmtLatency(d))}
	})
}
