// Package tui is the launcher: hosts, then sessions, one list at a time, with
// dialogs and the command palette over it (docs/contributing/ux-spec.md).
//
// The model never blocks: every request to a host runs in a tea.Cmd and
// reports back with a message. Attaching hands the whole terminal to
// client.Attach while the launcher stands down, and the launcher comes back
// when the user detaches.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

var (
	isTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
	runProgram = func(ctx context.Context, m tea.Model, in io.Reader, out io.Writer) error {
		if _, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run(); err != nil {
			return fmt.Errorf("run the launcher: %w", err)
		}
		return nil
	}
)

// Run opens the launcher, with no providers for the guided setup.
func Run(ctx context.Context) error { return RunWith(ctx, Options{}) }

// RunWith opens the launcher with options; Motion is set from the settings.
func RunWith(ctx context.Context, o Options) error {
	return runWith(ctx, realBackend{local: o.Local}, o)
}

func runWith(ctx context.Context, be Backend, o Options) error {
	what := "the launcher"
	hint := brand.Name + " ls (lists the sessions without the launcher)"
	if o.Switcher {
		what = "the switcher"
		hint = brand.Name + " flip <host>/<session> (names a session without the switcher)"
	}
	return runNamed(ctx, be, o, what, hint)
}

func runNamed(ctx context.Context, be Backend, o Options, what, hint string) error {
	in, out := launcherIn(), launcherOut()
	if !isTerminal(in) || !isTerminal(out) || !isTerminal(os.Stderr) {
		return &client.UserError{Msg: what + " needs a terminal", Hint: hint, Code: "no_terminal", Kind: client.KindUsage}
	}
	if !client.InputAllowed(ctx) {
		return &client.UserError{
			Msg:  what + " cannot open with input off",
			Hint: hint,
			Code: "no_input",
			Kind: client.KindNeedsAnswer,
		}
	}
	o.Motion = motionOn(be.Settings().UI.Motion, os.Getenv)
	m := New(ctx, be, o)
	m.handoff = true
	defer m.Close()
	prog := timeFrames(m)
	resumed := false
	for {
		li, err := startLauncherInput(in)
		if err != nil {
			if resumed {
				return resumeInputError(err)
			}
			return err
		}
		m.stopInput = li.finish
		err = runProgram(ctx, prog, li, out)
		stuck := li.finish()
		li.restoreTerminal()
		m.pauseBg()
		if err != nil && ctx.Err() != nil {
			return nil // stopped by a signal; sessions keep running
		}
		if err != nil {
			return err
		}
		if m.inputErr != nil {
			return m.inputErr
		}

		if m.pendingExec != nil {
			prog, err = m.runPendingExec(ctx, in, out, stuck)
		} else {
			prog, err = m.runPendingAttach(ctx, in, out, stuck)
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil || prog == nil {
			return err
		}
		resumed = true

	}
}

// motionOn applies ui.motion. "auto" turns motion off inside SSH sessions,
// in Emacs and JetBrains terminals, and with NO_COLOR or TERM=dumb.
func motionOn(setting string, getenv func(string) string) bool {
	switch setting {
	case "on":
		return true
	case "off":
		return false
	}
	switch {
	case getenv("SSH_CONNECTION") != "", getenv("SSH_TTY") != "",
		getenv("INSIDE_EMACS") != "",
		strings.Contains(getenv("TERMINAL_EMULATOR"), "JetBrains"),
		getenv("NO_COLOR") != "", getenv("TERM") == "dumb":
		return false
	}
	return true
}

// View draws the screen. The tab title counts the sessions that need you,
// so another tab can see it.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = brand.DisplayName
	if h := m.selectedHost(); h != nil && m.screen == scrSessions {
		v.WindowTitle += " · " + shown(h.name())
	}
	if n := m.needYou(); n > 0 {
		v.WindowTitle = fmt.Sprintf("● %d · %s", n, v.WindowTitle)
	}
	return v
}

// needYou counts the sessions that wait for the user, on every host.
func (m *Model) needYou() int {
	n := 0
	for _, h := range m.hosts {
		for _, s := range h.sessions {
			if s.State == "needs_input" {
				n++
			}
		}
	}
	return n
}

// render draws the whole screen: the launcher or the first-run screen and
// their motion, then a sheet over a dimmed backdrop.
func (m *Model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	c := lipgloss.NewCanvas(m.width, m.height)
	if len(m.hosts) == 0 {
		drawAt(c, m.viewFirstRun(), 0, 0)
	} else {
		drawAt(c, m.viewLauncher(), 0, 0)
		g := m.geo
		// The selection's tint glides from the row it left.
		if g.sel >= 0 {
			m.tint(c, g.x, g.listY+g.sel, g.w, m.glide())
		}
		if g.from >= 0 {
			m.tint(c, g.x, g.listY+g.from, g.w, 1-m.glide())
		}
		// Rows fade in, one after another, then the line under them.
		for i := range g.listN {
			m.fade(c, g.x, g.listY+i, g.w, m.rowAlpha(i))
		}
		if g.detailY > 0 {
			m.fade(c, g.x, g.detailY, g.w, m.rowAlpha(g.listN+1))
		}
		if m.modal == nil && m.glowing() {
			m.scan(c, g.x, ruleY, g.w)
		}
		// Notices rise above the bottom rule, sliding in from the left.
		notes := m.noticeLines(g.w - 4)
		for i, n := range notes {
			y := m.height - footRows - len(notes) + i
			dx := 0
			if m.motion {
				dx = int((1 - ease(float64(m.since(n.at))/float64(noticeIn))) * 6)
			}
			drawAt(c, n.s, g.x+2+dx, y)
			m.fade(c, g.x+2, y, g.w-2, n.alpha)
		}
		m.sweep(c, g, g.sel)
	}
	if m.modal != nil {
		dim(c)
		b := m.modal.view(m)
		bw, bh := lipgloss.Size(b)
		x, y := m.x0(), m.height-bh+m.sheetRise(bh)
		if len(m.hosts) == 0 {
			x = max((m.width-bw)/2, 0)
		}
		y = max(y, 0)
		drawAt(c, b, x, y)
		m.modalGeo = boxGeo{x: x, y: y, w: bw, h: bh}
	}
	return c.Render()
}

// drawAt draws a block of text on the canvas with its top left at x, y.
func drawAt(c *lipgloss.Canvas, s string, x, y int) {
	w, h := lipgloss.Size(s)
	uv.NewStyledString(s).Draw(c, uv.Rect(x, y, w, h))
}

// Attach.

// funcExec is a tea.ExecCommand that runs a function with the terminal.
type funcExec struct {
	in, out *os.File
	li      *launcherInput
	run     func(in, out *os.File) error
}

func (f *funcExec) SetStdin(r io.Reader) {
	switch v := r.(type) {
	case *os.File:
		f.in = v
	case *launcherInput:
		f.in = v.in
		f.li = v
	}
}

func (f *funcExec) SetStdout(w io.Writer) {
	if file, ok := w.(*os.File); ok {
		f.out = file
	}
}

func (f *funcExec) SetStderr(io.Writer) {}

func (f *funcExec) Run() (err error) {
	if f.li != nil {
		if err := f.li.pauseForExec(); err != nil {
			return resumeInputError(err)
		}
		defer func() {
			if rerr := f.li.resumeAfterExec(); rerr != nil {
				err = errors.Join(resumeInputError(rerr), err)
			}
		}()
	}
	in, out := f.in, f.out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	return f.run(in, out)
}

type pendingAttach struct {
	opts           client.AttachOptions
	host, id, name string
}

type resumedModel struct {
	m    *Model
	done attachDoneMsg
}

func (r *resumedModel) Init() tea.Cmd { return r.m.resumeAfterAttach(r.done) }
func (r *resumedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := r.m.Update(msg)
	return r, cmd
}
func (r *resumedModel) View() tea.View { return r.m.View() }

// attachTo hands the terminal to the session until the user detaches.
func (m *Model) attachTo(h *hostConn, id, name string) tea.Cmd {
	if m.handingOff() || m.jacking != nil {
		return nil
	}
	name = client.CleanLine(name) // only drawn, in the notices after the attach
	o := client.AttachOptions{
		Host: h.host, Session: id, Settings: m.settings, State: m.attach,
		// The launcher has let go of the terminal, so SSH may prompt.
		Interactive: true,
	}
	m.mem.Last = &lastSession{Host: h.name(), ID: id, Name: name, At: m.now()}
	save := m.remember()
	var res client.AttachResult
	var aerr error
	be, ctx, host := m.be, m.ctx, h.name()
	save = func(sessions func()) func() {
		return func() { sessions(); be.RememberLast(host, id, name) }
	}(save)
	if m.handoff {
		save()
		m.pendingAttach = &pendingAttach{opts: o, host: host, id: id, name: name}
		return m.quitForHandoff()
	}
	x := &funcExec{run: func(in, out *os.File) error {
		o.In, o.Out = in, out
		res, aerr = be.Attach(ctx, o)
		return nil
	}}
	return tea.Batch(func() tea.Msg { save(); return nil }, m.execWithInput(x, func(err error) tea.Msg {
		if aerr != nil {
			err = aerr
		}
		return attachDoneMsg{host: host, id: id, name: name, res: res, err: err}
	}))
}

func writeAttachEnd(w io.Writer, name, host string, r client.AttachResult) {
	name, host = client.CleanLine(name), client.CleanLine(host)
	switch e := r.Ended; {
	case e == nil:
		_, _ = fmt.Fprintf(w, "\r\nDetached. %s keeps running on %s.\n", name, host)
	case e.ExitCode == -1:
		_, _ = fmt.Fprintf(w, "\r\n%s on %s stopped.\n", name, host)
	case e.OOM:
		_, _ = fmt.Fprintf(w, "\r\n%s on %s ended (out of memory).\n", name, host)
	case e.Signal != "":
		_, _ = fmt.Fprintf(w, "\r\n%s on %s ended (%s).\n", name, host, client.CleanLine(e.Signal))
	default:
		_, _ = fmt.Fprintf(w, "\r\n%s on %s ended (exit %d).\n", name, host, e.ExitCode)
	}
}

func (m *Model) onAttachDone(msg attachDoneMsg) tea.Cmd {
	m.revealAt = m.now() // the rows fade back in
	var cmds []tea.Cmd
	if h := m.hostByName(msg.host); h != nil {
		cmds = append(cmds, m.listSessions(h))
	}
	if msg.err != nil {
		return tea.Batch(append(cmds, m.toastErr(msg.err))...)
	}
	r := msg.res
	name, host := shown(msg.name), shown(msg.host)
	if r.Reconnects > 0 {
		text := fmt.Sprintf("Reconnected to %s.", host)
		if r.Missed > 0 {
			text = fmt.Sprintf("Reconnected to %s (missed %s, replayed).", host, fmtSize(r.Missed))
		}
		cmds = append(cmds, m.toast(toastInfo, text))
	}
	var text string
	kind := toastSuccess
	switch e := r.Ended; {
	case e == nil:
		text = fmt.Sprintf("Detached. %s keeps running on %s.", name, host)
	case e.ExitCode == -1:
		text, kind = fmt.Sprintf("%s on %s stopped.", name, host), toastWarning
	case e.OOM:
		text, kind = fmt.Sprintf("%s on %s ended (out of memory).", name, host), toastWarning
	case e.Signal != "":
		text, kind = fmt.Sprintf("%s on %s ended (%s).", name, host, shown(e.Signal)), toastWarning
	default:
		text = fmt.Sprintf("%s on %s ended (exit %d).", name, host, e.ExitCode)
		if e.ExitCode != 0 {
			kind = toastWarning
		}
	}
	if hint := endHint(r.Ended); hint != "" {
		return tea.Batch(append(cmds, m.toastNext(kind, text, hint))...)
	}
	return tea.Batch(append(cmds, m.toast(kind, text))...)
}
