package tui

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/bundle"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// The launcher's debug commands (decision 0025): debug: start, debug: stop,
// debug: save and debug: bundle, in the palette, with no default key. While
// the launcher's own debug is on, its header shows debug and the minutes
// left. Nothing here works while attached: there is no key for it.

// debugBackend is the debug mode of the launcher's own process, and the
// bundle. The real backend has it; the demo has not.
type debugBackend interface {
	// DebugFile is the launcher's own debug file, or "" while its debug is
	// off.
	DebugFile() string
	// StartDebug turns the launcher's own debug on, with every category,
	// and returns its file. StopDebug turns it off.
	StartDebug() (string, error)
	StopDebug() error
	// SaveFlight saves the window of the launcher's flight recorder.
	SaveFlight() (string, error)
	// Bundle writes the bundle of a host, or of one of its sessions, in
	// the folder where the launcher started.
	Bundle(ctx context.Context, h client.Host, session string) (*bundle.Result, error)
}

// debugConn is what the debug commands ask of a connection.
type debugConn interface {
	DebugStart(ctx context.Context, id string, cats diag.Cats, d time.Duration) (client.DebugState, error)
	DebugStop(ctx context.Context, id string) (client.DebugState, error)
	DebugSave(ctx context.Context, id string) (client.DebugFileInfo, error)
	StartConnDebug(ctx context.Context, cats diag.Cats) (client.DebugState, error)
	StopConnDebug(ctx context.Context) error
}

var _ debugConn = (*client.Conn)(nil)

func (realBackend) DebugFile() string {
	if f := client.DebugFile(); f != nil {
		return f.Path()
	}
	return ""
}

func (realBackend) StartDebug() (string, error) {
	return client.StartDebug(client.DebugOptions{Cats: diag.AllCats})
}

func (realBackend) StopDebug() error {
	var warn bytes.Buffer
	client.StopDebug(&warn)
	if msg, hint, ok := strings.Cut(strings.TrimSpace(warn.String()), "\nTry: "); ok {
		return &client.UserError{Msg: strings.TrimPrefix(msg, "Warning: "), Hint: hint}
	}
	return nil
}

func (realBackend) SaveFlight() (string, error) { return client.SaveDebugFlight() }

func (realBackend) Bundle(ctx context.Context, h client.Host, session string) (*bundle.Result, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("find the launcher's folder: %w", err)
	}
	return bundle.Write(ctx, bundle.Options{Host: &h, Session: session, Dir: dir})
}

// launcherDebug is the debug that the palette's commands turned on.
type launcherDebug struct {
	// until is when what debug: start turned on stops by itself.
	until time.Time
	// own is true when debug: start turned on the launcher's own debug.
	// Debug that --debug turned on lasts as long as the launcher.
	own      bool
	ticking  bool      // a debugTickMsg is on its way
	sessions []sessKey // the sessions whose debug debug: start turned on
	hosts    []string  // the hosts whose connection's debug it turned on
	bundling *bundleRun
}

// debugTickMsg comes when the minutes left in the header change, and at
// the end of what debug: start turned on.
type debugTickMsg struct{}

// debugDoneMsg is the end of a debug request: a notice, and a path to show
// under it, or an error.
type debugDoneMsg struct {
	ok, path string
	err      error
	started  *sessKey // a debug: start that worked: the session, or the host alone
	bundled  bool     // the end of debug: bundle
}

// debugCommands are the palette's debug commands, which have no default
// key. A user can bind one under [keymap].
func debugCommands() []*command {
	need := func(m *Model) string {
		if _, ok := m.be.(debugBackend); !ok {
			return "the demo has no debug mode"
		}
		if h, _ := m.debugTarget(); h == nil {
			return "choose a host or a session first"
		}
		return ""
	}
	stop := func(m *Model) string {
		if be, ok := m.be.(debugBackend); ok && (be.DebugFile() != "" || len(m.dbg.sessions)+len(m.dbg.hosts) > 0) {
			return ""
		}
		return need(m)
	}
	return []*command{
		{noun: "debug", verb: "start", short: "start debug", why: need, run: (*Model).debugStart},
		{noun: "debug", verb: "stop", short: "stop debug", why: stop, run: (*Model).debugStop},
		{noun: "debug", verb: "save", short: "save the flight recorder", why: need, run: (*Model).debugSave},
		{noun: "debug", verb: "bundle", short: "write a debug bundle", why: need, run: (*Model).debugBundle},
	}
}

// debugTarget is what the debug commands act on: the selected session and
// its host, or the host in play.
func (m *Model) debugTarget() (*hostConn, *client.Session) {
	if h, s := m.selectedPair(); s != nil {
		return h, s
	}
	return m.selectedHost(), nil
}

// debugConnOf is h's connection, for a debug request, or an error that
// says why there is none.
func debugConnOf(h *hostConn) (debugConn, error) {
	if !h.online() {
		return nil, offlineErr(h)
	}
	dc, ok := h.conn.(debugConn)
	if !ok {
		return nil, &client.UserError{Msg: "the connection to " + h.name() + " has no debug mode",
			Hint: brand.Name + " host add " + h.name() + "   (updates the helper)"}
	}
	return dc, nil
}

// debugStart turns on the debug of the selected session, or that of the
// connection to the selected host. Once that works, it turns on the
// launcher's own debug too, for 30 minutes.
func (m *Model) debugStart() tea.Cmd {
	h, s := m.debugTarget()
	dc, err := debugConnOf(h)
	if err != nil {
		return m.toastErr(err)
	}
	name, ctx := h.name(), m.ctx
	if s == nil {
		return func() tea.Msg {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := dc.StartConnDebug(cctx, diag.AllCats)
			return debugDoneMsg{ok: "Debug is on for the launcher and its connection to " + name + ".", err: err, started: &sessKey{host: name}}
		}
	}
	key, sname := sessKey{name, s.ID}, s.Name
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		st, err := dc.DebugStart(cctx, key.id, diag.HostCats, api.DebugFor)
		until := ""
		if st.Until != nil {
			until = " until " + st.Until.Local().Format("15:04")
		}
		return debugDoneMsg{ok: fmt.Sprintf("Debug is on for the launcher, and for %s on %s%s.", sname, name, until), err: err, started: &key}
	}
}

// onDebugStarted turns on the launcher's own debug, once the host's side
// has started, and sets the end of both.
func (m *Model) onDebugStarted(msg debugDoneMsg) tea.Cmd {
	be := m.be.(debugBackend)
	if be.DebugFile() == "" {
		if _, err := be.StartDebug(); err != nil {
			return m.toastErr(err)
		}
		m.dbg.own = true
	}
	m.dbg.until = m.now().Add(api.DebugFor) // a second start sets a new end
	if k := *msg.started; k.id == "" {
		m.dbg.hosts = appendNew(m.dbg.hosts, k.host)
	} else {
		m.dbg.sessions = appendNew(m.dbg.sessions, k)
	}
	return tea.Batch(m.debugTick(), m.toast(toastSuccess, msg.ok))
}

// debugStop turns off the launcher's own debug, and the debug that
// debug: start turned on. With nothing that debug: start turned on, it
// turns off the selected session's debug, or that of the connection to
// the selected host.
func (m *Model) debugStop() tea.Cmd {
	be := m.be.(debugBackend)
	var cmds []tea.Cmd
	if be.DebugFile() != "" {
		if err := be.StopDebug(); err != nil {
			cmds = append(cmds, m.toastErr(err))
		}
	}
	sessions, hosts := m.dbg.sessions, m.dbg.hosts
	m.dbg = launcherDebug{ticking: m.dbg.ticking, bundling: m.dbg.bundling}
	if len(sessions)+len(hosts) == 0 {
		if h, s := m.debugTarget(); s != nil {
			sessions = []sessKey{{h.name(), s.ID}}
		} else if h != nil {
			hosts = []string{h.name()}
		}
	}
	cmds = append(cmds, m.toast(toastSuccess, "Debug is off."))
	for _, k := range sessions {
		cmds = append(cmds, m.debugOp(k.host, func(ctx context.Context, dc debugConn) error {
			_, err := dc.DebugStop(ctx, k.id)
			return err
		}))
	}
	return tea.Batch(append(cmds, m.stopConnDebug(hosts)...)...)
}

// stopConnDebug turns off the debug of the connections to hosts.
func (m *Model) stopConnDebug(hosts []string) []tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(hosts))
	for _, name := range hosts {
		cmds = append(cmds, m.debugOp(name, func(ctx context.Context, dc debugConn) error { return dc.StopConnDebug(ctx) }))
	}
	return cmds
}

// debugOp runs one debug request on a host's connection, and shows only an
// error.
func (m *Model) debugOp(host string, fn func(context.Context, debugConn) error) tea.Cmd {
	h := m.hostByName(host)
	if h == nil {
		return nil
	}
	dc, err := debugConnOf(h)
	if err != nil {
		return m.toastErr(err)
	}
	ctx := m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return debugDoneMsg{err: fn(cctx, dc)}
	}
}

// debugSave saves the flight recorder's window of the launcher, while its
// debug is on, and of the selected session.
func (m *Model) debugSave() tea.Cmd {
	be := m.be.(debugBackend)
	h, s := m.debugTarget()
	var cmds []tea.Cmd
	if be.DebugFile() != "" {
		cmds = append(cmds, func() tea.Msg {
			p, err := be.SaveFlight()
			return debugDoneMsg{ok: "Saved the launcher's flight recorder:", path: p, err: err}
		})
	}
	if s != nil {
		dc, err := debugConnOf(h)
		if err != nil {
			return tea.Batch(append(cmds, m.toastErr(err))...)
		}
		name, id, sname, ctx := h.name(), s.ID, s.Name, m.ctx
		cmds = append(cmds, func() tea.Msg {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			f, err := dc.DebugSave(cctx, id)
			return debugDoneMsg{ok: fmt.Sprintf("Saved the flight recorder of %s on %s:", sname, name), path: f.Path, err: err}
		})
	}
	if len(cmds) == 0 {
		return m.toastNext(toastInfo, "Nothing to save: no debug and no session.", "Run debug: start first.")
	}
	return tea.Batch(cmds...)
}

// onDebugMsg shows how a debug request went, and keeps the header's mark.
func (m *Model) onDebugMsg(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(debugTickMsg); ok {
		return m.onDebugTick()
	}
	d, _ := msg.(debugDoneMsg)
	if d.bundled {
		m.dbg.bundling = nil
	}
	switch {
	case d.err != nil:
		return m.toastErr(d.err)
	case d.started != nil:
		return m.onDebugStarted(d)
	case d.path != "":
		return m.toastNext(toastSuccess, d.ok, shortPath(localPath(d.path), m.cw()-8))
	case d.ok != "":
		return m.toast(toastSuccess, d.ok)
	}
	return nil
}

// debugMark is the header's mark while the launcher's own debug is on:
// debug, and when full, the minutes left when it stops by itself. While a
// bundle is being written, the mark says so.
func (m *Model) debugMark(full bool) string {
	be, ok := m.be.(debugBackend)
	if !ok {
		return ""
	}
	var parts []string
	if be.DebugFile() != "" {
		text := "debug"
		if full && m.dbg.own && !m.dbg.until.IsZero() {
			text += fmt.Sprintf(" %dm", max(int(math.Ceil(m.dbg.until.Sub(m.now()).Minutes())), 1))
		}
		parts = append(parts, text)
	}
	if m.dbg.bundling != nil {
		parts = append(parts, "bundling…")
	}
	if len(parts) == 0 {
		return ""
	}
	return "  " + sAccent.Render(strings.Join(parts, " · "))
}

// debugTick waits until the minutes left in the header change, or until
// what debug: start turned on ends. Only these ticks repaint the mark:
// when nothing moves, nothing repaints. Without minutes in the mark, as
// with --debug, only the end ticks.
func (m *Model) debugTick() tea.Cmd {
	if m.dbg.until.IsZero() || m.dbg.ticking {
		return nil
	}
	left := m.dbg.until.Sub(m.now())
	next := max(left, 0)
	if m.dbg.own {
		if next = left % time.Minute; next <= 0 {
			next = min(time.Minute, max(left, 0))
		}
	}
	m.dbg.ticking = true
	return m.after(max(next, time.Millisecond), debugTickMsg{})
}

// onDebugTick ends what debug: start turned on when its time has come: the
// launcher's own debug, and that of its connections. A session's holder
// ends its own.
func (m *Model) onDebugTick() tea.Cmd {
	m.dbg.ticking = false
	if m.dbg.until.IsZero() {
		return nil
	}
	if m.now().Before(m.dbg.until) {
		return m.debugTick()
	}
	cmds := []tea.Cmd{m.toast(toastInfo, "Debug is off: its 30 minutes ended.")}
	if m.dbg.own {
		if err := m.be.(debugBackend).StopDebug(); err != nil {
			cmds = append(cmds, m.toastErr(err))
		}
	}
	cmds = append(cmds, m.stopConnDebug(m.dbg.hosts)...)
	m.dbg = launcherDebug{bundling: m.dbg.bundling}
	return tea.Batch(cmds...)
}

// debugOnStart says where the launcher's debug file is, when --debug
// turned its debug on: the launcher prints no line before it starts.
func (m *Model) debugOnStart() tea.Cmd {
	be, ok := m.be.(debugBackend)
	if !ok || be.DebugFile() == "" {
		return nil
	}
	return m.toastNext(toastInfo, "Debug is on. The launcher writes to:", shortPath(localPath(be.DebugFile()), m.cw()-8))
}

// appendNew adds v to list, unless it is there.
func appendNew[T comparable](list []T, v T) []T {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}
