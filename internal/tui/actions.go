package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/wall"
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
	if h.status == stConnecting {
		return &client.UserError{Msg: "still connecting to " + h.name(), Hint: "Wait a moment, then try again.", Code: "connecting"}
	}
	return &client.UserError{Msg: h.name() + " is offline", Hint: fmt.Sprintf("%s host test %s", brand.Name, h.name()), Code: "offline"}
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
		m.openHost(h)
	}
	return nil
}

// resolveStartWall resolves the target wall level for r, memoizing any
// unpinned host probe so a wall_floor downgrade sheet never probes twice.
func resolveStartWall(ctx context.Context, c Conn, host client.Host, s client.Settings, r *startReq) (wallNotice, unpinnedHost string, prompt *downgradeInfo, err error) {
	var probed bool
	var probedLvl wall.Level
	var probedErr error
	probe := func() (wall.Level, error) {
		if !probed {
			probedLvl, probedErr = probeConnWall(ctx, c)
			probed = true
		}
		return probedLvl, probedErr
	}
	plan, werr := client.ResolveWall(host, s, r.args.Wall, r.args.Downgrade, probe)
	if werr != nil {
		var ue *client.UserError
		if errors.As(werr, &ue) && ue.Code == "wall_floor" {
			if dp, derr := client.ResolveWall(host, s, r.args.Wall, true, probe); derr == nil {
				return "", "", &downgradeInfo{
					host:   host.Name,
					floor:  dp.Floor,
					from:   dp.DowngradedFrom,
					want:   dp.Level,
					shared: client.IsSharedHost(host),
				}, nil
			}
		}
		return "", "", nil, werr
	}
	r.args.Wall = plan.Level
	r.args.Downgrade = plan.Downgraded
	r.args.WallFloor = plan.Floor
	r.args.DowngradedFrom = plan.DowngradedFrom
	r.wallResolved = true
	if plan.UnpinnedNotice != "" {
		return plan.UnpinnedNotice, host.Name, nil, nil
	}
	return plan.RaisedNotice, "", nil, nil
}

// startSession sends "new" to the host h, for the start r.
func (m *Model) startSession(h *hostConn, r startReq) tea.Cmd {
	r.host = h.name()
	// A name that looks like an ID fails before any secret is fetched.
	if err := client.CheckSessionName(r.args.Name); err != nil {
		return func() tea.Msg { return startedMsg{startReq: r, err: err} }
	}
	if !h.online() {
		err := offlineErr(h)
		return func() tea.Msg { return startedMsg{startReq: r, err: err} }
	}
	s, warnCmd := m.refreshSettings()
	r.args = client.SessionArgs(r.args, m.width, m.height, s)
	c, be, host := h.conn, m.be, h.host
	return tea.Batch(warnCmd, m.bgCmd(func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var wallNotice, unpinnedHost string
		if !r.wallResolved {
			var prompt *downgradeInfo
			var werr error
			wallNotice, unpinnedHost, prompt, werr = resolveStartWall(cctx, c, host, s, &r)
			if prompt != nil {
				return startedMsg{startReq: r, downgradePrompt: prompt}
			}
			if werr != nil {
				return startedMsg{startReq: r, err: werr}
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
			secrets, err := be.Secrets(cctx, host)
			if err != nil {
				return startedMsg{startReq: r, err: err, warn: warn, wallNotice: wallNotice, unpinnedHost: unpinnedHost}
			}
			r.args.Secrets = secrets
		}
		sess, err := c.NewSession(cctx, r.args)
		return startedMsg{startReq: r, s: sess, err: err, warn: warn, wallNotice: wallNotice, unpinnedHost: unpinnedHost}
	}))
}

// onStarted shows how a start went, and a warning about the user's answers
// about setup scripts, when they could not be read.
func (m *Model) onStarted(msg startedMsg) tea.Cmd {
	cmd := m.showStarted(msg)
	var cmds []tea.Cmd
	if msg.wallNotice != "" {
		show := true
		if msg.unpinnedHost != "" {
			if m.unpinnedNoted[msg.unpinnedHost] {
				show = false
			} else {
				m.unpinnedNoted[msg.unpinnedHost] = true
			}
		}
		if show {
			cmds = append(cmds, m.toast(toastInfo, msg.wallNotice))
		}
	}
	if w := msg.warn; w != nil {
		cmds = append(cmds, m.toastSetupWarn(w))
	}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// showStarted shows how a start went, in the form or the quick-start line
// that waits for it, or opens the host's question about it. After esc,
// nothing waits: the launcher asks nothing, and does not attach to the
// session.
func (m *Model) showStarted(msg startedMsg) tea.Cmd {
	d, p := m.waiting(msg.startReq)
	waited := d != nil || p != nil
	if msg.downgradePrompt != nil {
		if waited {
			cleanup := m.disposeModal()
			m.modal = newDowngradeAsk(msg.startReq, *msg.downgradePrompt)
			return cleanup
		}
		return nil
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
	return tea.Batch(m.toast(toastSuccess, fmt.Sprintf("Started %s on %s.", msg.s.Name, h.name())), m.listSessions(h))
}

func (m *Model) attachSelected() tea.Cmd {
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

type shellOpenedMsg struct {
	h        *hostConn
	hostName string
	conn     Conn
	s        *client.Session
	created  bool
	err      error
}

// openSandboxShell opens or attaches to a shell inside the selected session's
// sandbox (or a host shell in the same folder for an unwalled session),
// retaining the original accepted host connection (cli-spec 15.2, isolation
// spec 6.11).
func (m *Model) openSandboxShell() tea.Cmd {
	h, s := m.selectedPair()
	if h == nil || s == nil {
		return nil
	}
	if ended(*s) {
		return m.toast(toastInfo, "The selected session has ended.")
	}
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	settings, warnCmd := m.refreshSettings()
	target := *s
	c, host, hostName, cols, rows := h.conn, h.host, h.name(), m.width, m.height
	return tea.Batch(warnCmd, m.bgCmd(func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		opts := client.ShellOptions{Cols: cols, Rows: rows, Settings: settings}
		if cc, ok := c.(*client.Conn); ok {
			sess, created, err := client.OpenShellSession(cctx, cc, target, opts)
			return shellOpenedMsg{h: h, hostName: hostName, conn: c, s: sess, created: created, err: err}
		}
		sess, created, err := openShellOnConn(cctx, c, host, target, opts)
		return shellOpenedMsg{h: h, hostName: hostName, conn: c, s: sess, created: created, err: err}
	}))
}

func openShellOnConn(ctx context.Context, c Conn, host client.Host, target client.Session, opts client.ShellOptions) (*client.Session, bool, error) {
	if !api.IsWalled(target.Wall) {
		args := client.UnwalledShellArgs(host, target, opts)
		s, err := c.NewSession(ctx, args)
		return s, err == nil, err
	}
	list, err := c.Sessions(ctx)
	if err != nil {
		return nil, false, err
	}
	if existing, ok := client.FindShellSession(list, target); ok {
		return &existing, false, nil
	}
	args := client.SessionArgs(client.NewArgs{
		In:    target.ID,
		Agent: "shell",
		Name:  opts.Name,
		Shell: opts.Settings.Sandbox.Shell,
	}, opts.Cols, opts.Rows, opts.Settings)
	s, err := c.NewSession(ctx, args)
	return s, err == nil, err
}

func (m *Model) onShellOpened(msg shellOpenedMsg) tea.Cmd {
	if msg.err != nil {
		return m.toastErr(msg.err)
	}
	if msg.s == nil {
		return nil
	}
	// Verify the original accepted host connection is still live and unchanged;
	// never resolve a new host under an old selection (cli-spec 15.2).
	var found *hostConn
	for _, h := range m.hosts {
		if h == msg.h && h.conn == msg.conn && h.name() == msg.hostName {
			found = h
			break
		}
	}
	if found == nil {
		return m.toastErr(&client.UserError{
			Msg:  msg.hostName + " changed while opening the shell",
			Hint: "Select the session again and retry.",
			Code: "host_changed",
		})
	}
	if msg.created {
		found.sessions = append(found.sessions, *msg.s)
		found.changes++
	}
	m.sel, m.autoSel = sessKey{found.name(), msg.s.ID}, false
	m.syncSel()
	return m.jack(found, msg.s.ID, msg.s.Name)
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
	if client.ShellWord(addr) != addr {
		return m.toastErr(&client.UserError{Msg: "the address of this session cannot go in a command", Hint: brand.Name + " ls", Code: "bad_address", Kind: client.KindUsage})
	}
	text := brand.Name + " attach " + addr
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
		ok := fmt.Sprintf("Deleted %s on %s.", client.CleanLine(s.Name), client.CleanLine(host))
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

// saveHosts writes the host list with one host added, replaced or removed,
// unless the launcher's context ends first: then it writes nothing.
func (m *Model) saveHosts(host client.Host, remove bool) tea.Cmd {
	be := m.be
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		hosts, err := be.LoadHosts(ctx)
		if err != nil {
			return m.completeHostSave(hostSavedMsg{host: host, remove: remove, err: err})
		}
		out := changedHosts(hosts, host, remove)
		return m.completeHostSave(hostSavedMsg{host: host, remove: remove, err: be.SaveHosts(ctx, out)})
	})
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
	return m.applyHostSaved(msg)
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
		return opDoneMsg{host: name, ok: fmt.Sprintf("%s answers in %s.", name, fmtLatency(d))}
	})
}
