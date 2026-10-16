package tui

import (
	"context"
	"slices"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// screen is the one list the launcher shows (docs/contributing/ux-spec.md 2): the hosts, or
// the sessions on one host.
type screen int

const (
	scrHosts screen = iota
	scrSessions
)

// sessKey names a session across hosts.
type sessKey struct{ host, id string }

// Options configure the launcher.
type Options struct {
	// Motion turns animation on. See motionOn.
	Motion bool
	// Now is the clock. Tests set it.
	Now func() time.Time
	// Switcher opens on the sessions of every host, for a quick switch.
	Switcher bool
	// Local is the guided setup's view of this computer. The command tree
	// passes one that knows its providers.
	Local onboard.Local
}

// Model is the launcher: one list at a time, and the dialogs drawn over it.
type Model struct {
	ctx      context.Context
	be       Backend
	settings client.Settings
	now      func() time.Time
	motion   bool

	width, height int
	dark          bool
	richColor     bool // 256 colours or more: the brand gradient shows
	truecolor     bool
	colors        termColors // the terminal's own colours, for smooth blends
	focused       bool       // the terminal window has focus

	hosts    []*hostConn
	loadErr  error
	mem      memory   // what the last run saw (memory.go)
	sshHosts []string // the Host aliases in the user's SSH config

	screen     screen
	cur        string // the host the sessions screen shows; "" for every host
	hostSel    int    // index into visibleHosts()
	hostTop    int
	sel        sessKey
	want       sessKey // a session to select as soon as it is listed
	selIdx     int
	autoSel    bool // the selection was not picked by the user, so it stays on top
	sessTop    int
	hideExited bool
	prompt     *promptState // the search or quick-start line, when open

	preview previewState

	rename    *renameState
	modal     modal
	toasts    toastStack
	quitArmed bool

	// Motion: when it all began, when the list last appeared, when the
	// dialog on top opened; ticking is true while a frame tick is pending.
	epoch    time.Time
	revealAt time.Time
	modalAt  time.Time
	quitAt   time.Time
	slideAt  time.Time
	slideDir int // +1 into a host's sessions, -1 back to the hosts
	selAt    time.Time
	selFrom  int // the row the selection left, for its glide
	jacking  *jackIn
	ticking  bool

	// logoClock is how far the logo's wave has run. Only a frame tick moves
	// it, so the logo stays still while nothing else moves.
	logoClock time.Duration

	traces     map[sessKey][]time.Time // moments of output, for each row's trace
	activity   map[sessKey]activity    // what each session is doing, from its screen
	activityAt time.Time               // when an activity line last changed
	geo        boxGeo                  // where the launcher's box was last drawn
	modalGeo   boxGeo                  // and the dialog on top

	// attach is kept for the whole run, so attaching to the same session
	// again continues from the byte the terminal already shows.
	attach    *client.AttachState
	lastAgent map[string]string
	conns     *connSet

	// exec hands the terminal to a blocking command; after delivers msg
	// after d. Tests replace both.
	exec  func(tea.ExecCommand, tea.ExecCallback) tea.Cmd
	after func(d time.Duration, msg tea.Msg) tea.Cmd
}

// renameState is the inline rename of a session row.
type renameState struct {
	key  sessKey
	old  string
	in   input
	busy bool
}

// New builds the launcher. It reads the saved hosts at once; everything
// that touches the network runs later, in commands.
func New(ctx context.Context, be Backend, o Options) *Model {
	m := &Model{
		ctx: ctx, be: be, now: o.Now, motion: o.Motion,
		width: 80, height: 24, dark: true, focused: true,
		autoSel:   true,
		attach:    &client.AttachState{},
		lastAgent: map[string]string{},
		traces:    map[sessKey][]time.Time{},
		activity:  map[sessKey]activity{},
		conns:     newConnSet(),
		exec:      tea.Exec,
		after: func(d time.Duration, msg tea.Msg) tea.Cmd {
			return tea.Tick(d, func(time.Time) tea.Msg { return msg })
		},
	}
	if m.now == nil {
		m.now = time.Now
	}
	m.epoch, m.revealAt = m.now(), m.now()
	m.settings = be.Settings()
	commands = withKeymap(registry(), m.settings.Keymap)
	hosts, err := be.LoadHosts()
	m.loadErr = err
	sort.SliceStable(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	for _, h := range hosts {
		m.hosts = append(m.hosts, &hostConn{host: h})
	}
	m.restore(o.Switcher)
	if d, ok := be.(interface {
		demoTraces(time.Time) map[sessKey][]time.Time
	}); ok {
		m.traces = d.demoTraces(m.now())
	}
	switch {
	case o.Switcher && len(m.hosts) > 0:
		m.screen, m.cur = scrSessions, ""
	case m.want != (sessKey{}):
		// Where the last run left off.
	case len(m.hosts) == 1:
		// One host: its sessions are the only useful list.
		m.screen, m.cur = scrSessions, m.hosts[0].name()
	}
	m.syncSel()
	return m
}

// Close closes every connection and saves what the launcher saw. Run calls
// it after the program ends.
func (m *Model) Close() {
	if g, ok := m.modal.(*guide); ok {
		g.halt() // a half-made machine is deleted before the launcher goes
	}
	m.remember()()
	m.conns.closeAll()
}

// Init starts the connections and the timers.
func (m *Model) Init() tea.Cmd {
	be := m.be
	cmds := []tea.Cmd{
		func() tea.Msg { return sshHostsMsg{be.SSHConfigHosts()} },
		tea.RequestBackgroundColor,
		tea.RequestForegroundColor,
		queryPalette(),
		m.after(refreshEvery, refreshTickMsg{}),
		m.after(latencyEvery, latencyTickMsg{}),
		m.after(activityEvery, activityTickMsg{}),
	}
	for _, h := range m.hosts {
		cmds = append(cmds, m.dial(h))
	}
	if m.loadErr != nil {
		msg, hint := errText(m.loadErr)
		m.toasts.push(m.now(), toastError, sentence(msg), hint)
		cmds = append(cmds, m.after(toastErrorLife, toastTickMsg{}))
	}
	return tea.Batch(append(cmds, m.startTimers())...)
}

// Timer and result messages.
type (
	previewTickMsg struct{ seq int }
	opDoneMsg      struct {
		host string
		ok   string
		err  error
		drop string // a session to take off the list
	}
	foldersMsg struct {
		host, prefix string
		res          client.FoldersResult
		quick        bool // for the quick-start line
	}
	activityTickMsg struct{}
	startedMsg      struct {
		startReq // what started: a question about it sends it again
		s        *client.Session
		err      error
	}
	attachDoneMsg struct {
		host, id, name string
		res            client.AttachResult
		err            error
	}
	hostSavedMsg struct {
		host   client.Host
		remove bool
		err    error
	}
	settingsMsg struct{ s client.Settings }
	sshHostsMsg struct{ hosts []string }
)

// Update handles one message. It never blocks: everything slow is a command.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before, modal, scr, cur, armed := m.sel, m.modal, m.screen, m.cur, m.quitArmed
	selIdx, hostSel := m.selIdx, m.hostSel
	cmd := m.update(msg)
	if m.sel != before {
		cmd = tea.Batch(cmd, m.schedulePreview())
	}
	// Moments that start an animation: a dialog opens, a list appears, the
	// quit chord arms.
	if m.modal != nil && m.modal != modal {
		m.modalAt = m.now()
	}
	if m.screen != scr || m.cur != cur {
		m.revealAt, m.slideAt, m.slideDir = m.now(), m.now(), 1
		if m.screen == scrHosts {
			m.slideDir = -1
		}
	} else if m.sel != before || m.hostSel != hostSel {
		m.selAt, m.selFrom = m.now(), selIdx
		if m.screen == scrHosts {
			m.selFrom = hostSel
		}
	}
	if m.quitArmed && !armed {
		m.quitAt = m.now()
	}
	return m, tea.Batch(cmd, m.startTimers())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.syncSel()
		return nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.colors.bg = msg.Color
		return nil
	case tea.ForegroundColorMsg:
		m.colors.fg = msg.Color
		return nil
	case uv.UnknownOscEvent:
		m.colors.onOSC(string(msg))
		return nil
	case tea.ColorProfileMsg:
		m.truecolor = msg.Profile == colorprofile.TrueColor
		m.richColor = m.truecolor || msg.Profile == colorprofile.ANSI256
		return nil
	case tea.FocusMsg:
		m.focused = true
		return nil
	case tea.BlurMsg:
		m.focused = false
		return nil
	case animTickMsg:
		m.ticking = false
		m.logoClock += msg.d
		return nil
	case jackMsg:
		return m.onJack()
	case activityMsg:
		m.onActivity(msg)
		return nil
	case activityTickMsg:
		return tea.Batch(m.after(activityEvery, activityTickMsg{}), m.readActivity())
	case tea.KeyPressMsg:
		return m.onKey(msg)
	case tea.MouseClickMsg:
		return m.onClick(msg)
	case tea.MouseWheelMsg:
		return m.onWheel(msg)
	case tea.PasteMsg:
		return m.onPaste(msg.Content)
	case toastTickMsg:
		return m.onToastTick()
	case dialledMsg, connDoneMsg, redialMsg, sessionsMsg, latencyMsg, doctorMsg, refreshTickMsg, latencyTickMsg:
		return m.onConn(msg)
	case previewTickMsg:
		if msg.seq == m.preview.seq {
			return m.peek()
		}
		return nil
	case peekMsg:
		m.onPeek(msg)
		return nil
	case opDoneMsg:
		return m.onOpDone(msg)
	case shellDoneMsg:
		if msg.err != nil {
			return m.toastErr(&client.UserError{Msg: "the shell on " + msg.host + " ended with an error: " + msg.err.Error()})
		}
		return nil
	case foldersMsg:
		return m.onFolders(msg)
	case startedMsg:
		return m.onStarted(msg)
	case attachDoneMsg:
		return m.onAttachEnded(msg)
	case hostSavedMsg:
		return m.onHostSaved(msg)
	case setupMsg:
		return m.onSetup(msg)
	case editorDoneMsg:
		return m.onEditorDone(msg)
	case settingsMsg:
		m.settings = msg.s
		commands = withKeymap(registry(), m.settings.Keymap)
		return nil
	case sshHostsMsg:
		return m.onSSHHosts(msg)
	}
	switch d := m.modal.(type) {
	case *wizard:
		return d.onMsg(m, msg)
	case *guide:
		return d.onMsg(m, msg)
	}
	return nil
}

// onToastTick drops expired toasts and waits for the next to expire.
func (m *Model) onToastTick() tea.Cmd {
	if m.toasts.expire(m.now()) {
		if d, ok := m.toasts.next(m.now()); ok {
			return m.after(d, toastTickMsg{})
		}
	}
	return nil
}

// onOpDone reports a finished operation, and lists the host's sessions again.
func (m *Model) onOpDone(msg opDoneMsg) tea.Cmd {
	var cmd tea.Cmd
	if msg.err != nil {
		cmd = m.toastErr(msg.err)
	} else if msg.ok != "" {
		cmd = m.toast(toastSuccess, msg.ok)
	}
	if h := m.hostByName(msg.host); h != nil {
		h.changes++
		if msg.err == nil && msg.drop != "" {
			h.sessions = slices.DeleteFunc(h.sessions, func(s client.Session) bool { return s.ID == msg.drop })
			m.syncSel()
		}
		cmd = tea.Batch(cmd, m.listSessions(h))
	}
	if m.rename != nil && m.rename.busy {
		m.rename = nil
	}
	return cmd
}

// onFolders takes folder completions, for the quick start or the form.
func (m *Model) onFolders(msg foldersMsg) tea.Cmd {
	if p := m.prompt; msg.quick && p != nil && p.host == msg.host && p.asked == msg.prefix {
		p.folders = msg.res.Matches
	}
	if d, ok := m.modal.(*newSession); ok && d.host == msg.host && !msg.quick {
		d.setSuggestions(msg.prefix, msg.res)
	}
	return nil
}

// onAttachEnded comes back from an attach, and tells the wizard if it waited for it.
func (m *Model) onAttachEnded(msg attachDoneMsg) tea.Cmd {
	cmd := m.onAttachDone(msg)
	if w, ok := m.modal.(*wizard); ok && w.waiting != "" && w.waiting == msg.id && w.host.Name == msg.host {
		cmd = tea.Batch(cmd, w.afterAttach(m, msg))
	}
	return cmd
}

// onSetup passes a setup step's result to the wizard, or drops it if the wizard closed.
func (m *Model) onSetup(msg setupMsg) tea.Cmd {
	if w, ok := m.modal.(*wizard); ok {
		return w.onMsg(m, msg)
	}
	if msg.conn != nil {
		return m.dropConn(msg.conn) // the wizard closed while it ran
	}
	return nil
}

// onEditorDone reloads the settings after the editor closes.
func (m *Model) onEditorDone(msg editorDoneMsg) tea.Cmd {
	be := m.be
	reload := func() tea.Msg { return settingsMsg{be.Settings()} }
	if msg.err != nil {
		return tea.Batch(reload, m.toastErr(msg.err))
	}
	return reload
}

// onSSHHosts keeps the hosts from the SSH config, for the add-host wizard.
func (m *Model) onSSHHosts(msg sshHostsMsg) tea.Cmd {
	m.sshHosts = msg.hosts
	if w, ok := m.modal.(*wizard); ok {
		w.known = msg.hosts
	}
	return nil
}

// needSpin reports whether something on screen moves slowly: a spinner, a
// working glyph or a breathing dot.
func (m *Model) needSpin() bool {
	if b, ok := m.modal.(interface{ busy() bool }); ok && b.busy() {
		return true
	}
	for _, h := range m.hosts {
		if h.status == stConnecting {
			return true
		}
		for _, s := range h.sessions {
			if s.State == "working" || s.State == "starting" || s.State == "needs_input" {
				return true
			}
		}
	}
	return false
}

// Hosts and sessions as shown.

func (m *Model) visibleHosts() []*hostConn { return m.hosts }

// selectedHost is the host in play: the one picked in the hosts list, or
// the one whose sessions are shown. It is nil on the list of every host.
func (m *Model) selectedHost() *hostConn {
	if m.screen == scrSessions {
		return m.hostByName(m.cur)
	}
	vh := m.visibleHosts()
	if m.hostSel < 0 || m.hostSel >= len(vh) {
		return nil
	}
	return vh[m.hostSel]
}
