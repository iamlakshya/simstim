package tui

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/iamlakshya/simstim/internal/brand"
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
	// After schedules delayed messages; tests replace it to run without wall-clock ticks.
	After func(time.Duration, tea.Msg) tea.Cmd
	// SessionShell runs the shared CLI shell command after terminal handoff.
	SessionShell SessionShellRunner
	// PolicyLockdown runs the real restrictive CLI action after handoff.
	PolicyLockdown func(context.Context, *os.File, *os.File) error
}

// Model is the launcher: one list at a time, and the dialogs drawn over it.
type Model struct {
	ctx                context.Context
	cancelTier         context.CancelFunc
	tierRequests       chan tierQuestionRequest
	grantRequests      chan grantQuestionRequest
	grantActive        chan bool
	grantOwners        grantOwners
	selectedOwners     selectedNewOwners
	selectedNew        *newSelectedStart
	sessionShell       SessionShellRunner
	policyLockdown     func(context.Context, *os.File, *os.File) error
	be                 Backend
	settings           client.Settings
	now                func() time.Time
	motion             bool
	settingsWarned     string
	settingsGeneration uint64

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
	newHost   newHostLookup
	resuming  map[sessKey]bool // the sessions whose resume runs (resume.go)
	conns     *connSet
	dbg       launcherDebug // what the debug commands turned on (debug.go)

	// exec hands the terminal to a blocking command; after delivers msg
	// after d. Tests replace both.
	exec          func(tea.ExecCommand, tea.ExecCallback) tea.Cmd
	after         func(d time.Duration, msg tea.Msg) tea.Cmd
	handoff       bool
	pendingAttach *pendingAttach
	pendingExec   *pendingExec
	stopInput     func() bool
	inputErr      error
	bgMu          sync.Mutex
	bgCtx         context.Context
	bgCancel      context.CancelFunc
	bgWG          sync.WaitGroup
	waiters       atomic.Int32
	hostSaves     hostSaveCompletions
	// tierWait lets the fake scheduler recognise an idle question reader.
	tierWait func(func() tea.Msg) tea.Msg
}

// renameState is the inline rename of a session row.
type renameState struct {
	key  sessKey
	old  string // the name as the host sent it; the field shows it cleaned
	in   input
	busy bool
	// edited is true once a key changed the field. Until then, Enter keeps
	// the host's name, hidden characters and all.
	edited bool
}

// New builds the launcher. It reads the saved hosts at once; everything
// that touches the network runs later, in commands.
func New(ctx context.Context, be Backend, o Options) *Model {
	// The launcher draws its own screen: a wait of the client, such as for
	// the lock of hosts.toml, writes nothing on it, and a host-ID change on
	// Dial surfaces as a toast rather than stderr text.
	ctx, cancelTier := context.WithCancel(ctx)
	m := &Model{
		ctx: ctx, be: be, sessionShell: o.SessionShell, policyLockdown: o.PolicyLockdown, now: o.Now, motion: o.Motion,
		cancelTier: cancelTier, tierRequests: make(chan tierQuestionRequest, 1),
		grantRequests: make(chan grantQuestionRequest, 1),
		width:         80, height: 24, dark: true, focused: true,
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
	ctx = m.launcherContext(ctx)
	m.ctx = ctx
	if o.After != nil {
		m.after = o.After
	}
	if m.now == nil {
		m.now = time.Now
	}
	m.epoch, m.revealAt = m.now(), m.now()
	m.settings, m.settingsWarned = be.ReadSettings()
	commands = withKeymap(registry(), m.settings.Keymap)
	m.hosts, m.loadErr = loadSavedHosts(ctx, be)
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
	m.openBg(ctx)
	m.syncSel()
	return m
}

// Init starts the connections and the timers.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.loadSSHHosts(),
		tea.RequestBackgroundColor,
		tea.RequestForegroundColor,
		queryPalette(),
		m.after(refreshEvery, refreshTickMsg{}),
		m.after(latencyEvery, latencyTickMsg{}),
		m.after(activityEvery, activityTickMsg{}),
		m.debugOnStart(),
		m.waitForTierQuestion(),
		m.waitForGrantQuestion(),
		m.keymapOnStart(),
	}
	for _, h := range m.hosts {
		cmds = append(cmds, m.dial(h))
	}
	if m.loadErr != nil {
		msg, hint := errText(m.loadErr)
		m.toasts.push(m.now(), toastError, sentence(msg), hint)
		cmds = append(cmds, m.after(toastErrorLife, toastTickMsg{}))
	}
	if m.settingsWarned != "" {
		cmds = append(cmds, m.toastNext(toastWarning, m.settingsWarned+".", brand.Name+" config check"))
	}
	return tea.Batch(append(cmds, m.startTimers())...)
}

// Update handles one message. It never blocks: everything slow is a command.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	quick := m.prompt
	quickText, quickSeq := "", uint64(0)
	if quick != nil {
		quickText, quickSeq = quick.in.value(), quick.folderSeq
	}
	before, modal, scr, cur, armed := m.sel, m.modal, m.screen, m.cur, m.quitArmed
	selIdx, hostSel := m.selIdx, m.hostSel
	// An action that read config.toml brings the settings first, so that
	// what it leads to, such as an attach, uses them.
	cmd := tea.Batch(m.settingsOf(msg), m.update(msg))
	m.retireQuickFolders(quick, quickText, quickSeq)
	m.retireFormFolders(modal)
	m.retirePolicy(modal)
	cmd = tea.Batch(cmd, m.retireSelectedNew())
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
	case execInputFailedMsg:
		m.inputErr = msg.err
		return tea.Quit
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
		if m.newHostLookupKey(msg.String()) {
			return nil
		}
		return m.onKey(msg)
	case tea.MouseClickMsg:
		m.cancelNewHostLookup()
		return m.onClick(msg)
	case tea.MouseWheelMsg:
		return m.onWheel(msg)
	case tea.PasteMsg:
		return m.onPaste(msg.Content)
	case toastTickMsg:
		return m.onToastTick()
	case debugTickMsg, debugDoneMsg:
		return m.onDebugMsg(msg)
	case dialledMsg, connDoneMsg, redialMsg, sessionsMsg, latencyMsg, doctorMsg, hostRuntimeUpgradedV1, refreshTickMsg, latencyTickMsg:
		return m.onConn(msg)
	case previewTickMsg:
		if msg.seq == m.preview.seq {
			return m.peek()
		}
		return nil
	case peekMsg:
		m.onPeek(msg)
		return nil
	case policyMsg:
		return m.onPolicy(msg)
	case policyStoppedMsg:
		return m.onPolicyStopped(msg)
	case opDoneMsg:
		return m.onOpDone(msg)
	case grantCleanupMsg:
		if msg.err != nil {
			return m.toastErr(msg.err)
		}
		return nil
	case grantQuestionRequest:
		return m.onGrantQuestion(msg)
	case grantQuestionEndedMsg:
		return m.onGrantQuestionEnded(msg)
	case tierQuestionRequest:
		return m.onTierQuestion(msg)
	case tierQuestionEndedMsg:
		return m.onTierQuestionEnded(msg)
	case hostTierResolvedMsg:
		return m.onHostTierResolved(msg)
	case setupWarnMsg:
		return m.toastSetupWarn(msg.warn)
	case shellDoneMsg:
		if msg.err != nil {
			return m.toastErr(&client.UserError{Msg: "the shell on " + msg.host + " ended with an error: " + msg.err.Error(), Hint: "Open the shell again.", Code: "shell_failed"})
		}
		return nil
	case foldersMsg:
		return m.onFolders(msg)
	case newSelectedReadyMsg:
		return m.onSelectedReady(msg)
	case newSelectedResultMsg:
		return m.onSelectedResult(msg)
	case newSelectedFinishedMsg:
		return m.onSelectedFinished(msg)
	case startedMsg:
		return m.onStarted(msg)
	case resumedMsg:
		return m.onResumed(msg)
	case attachDoneMsg:
		return m.onAttachEnded(msg)
	case hostSavedMsg:
		return m.onHostSaved(msg)
	case newStartSourceMsg:
		return m.onNewStartSource(msg)
	case newStartDialledMsg:
		return m.onNewStartDialled(msg)
	case newHostResolvedMsg:
		return m.onNewHostResolved(msg)
	case newHostTierResolvedMsg:
		return m.onNewHostTierResolved(msg)
	case hostReloadedMsg:
		return m.onHostReloaded(msg)
	case setupMsg:
		return m.onSetup(msg)
	case rootSealedMsg:
		return m.onRootSealed(msg)
	case editorDoneMsg:
		return m.onEditorDone(msg)
	case settingsMsg:
		return m.onSettings(msg)
	case sshHostsMsg:
		return m.onSSHHosts(msg)
	}
	switch d := questionOwner(m.modal).(type) {
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

// onAttachEnded comes back from an attach, and tells the wizard if it waited for it.
func (m *Model) onAttachEnded(msg attachDoneMsg) tea.Cmd {
	cmd := m.onAttachDone(msg)
	if w, ok := m.modal.(*wizard); ok && w.waiting != "" && w.waiting == msg.id && w.host.Name == msg.host {
		cmd = tea.Batch(cmd, w.afterAttach(m, msg))
	}
	return cmd
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
