package tui

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// execInputError distinguishes a terminal that cannot resume from an editor
// or shell command that merely failed. Its callback must leave the launcher.
type execInputError struct{ cause error }

func (e execInputError) Error() string { return e.cause.Error() }
func (e execInputError) Unwrap() error { return e.cause }

type execInputFailedMsg struct{ err error }

func (m *Model) execWithInput(x *funcExec, after tea.ExecCallback) tea.Cmd {
	if m.handingOff() || m.jacking != nil {
		return nil
	}
	if m.handoff {
		m.pendingExec = &pendingExec{command: x, after: after}
		return m.quitForHandoff()
	}
	return m.exec(x, func(err error) tea.Msg {
		var failed execInputError
		if errors.As(err, &failed) {
			return execInputFailedMsg{err: err}
		}
		return after(err)
	})
}

func resumeInputError(err error) error {
	var ue *client.UserError
	if errors.As(err, &ue) {
		return execInputError{cause: err}
	}
	return execInputError{cause: fmt.Errorf("resume the launcher input: %w", errors.Join(&client.UserError{
		Msg:  "the terminal's reader could not restart",
		Hint: "Open a new terminal and run " + brand.Name + ".",
		Code: "terminal_failed",
	}, err))}
}

// pendingExec runs only after the program has returned the terminal.
type pendingExec struct {
	command *funcExec
	after   tea.ExecCallback
}

type resumedExecModel struct {
	m    *Model
	done tea.Msg
}

func (r *resumedExecModel) Init() tea.Cmd { return r.m.resumeAfterExec(r.done) }
func (r *resumedExecModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := r.m.Update(msg)
	return r, cmd
}
func (r *resumedExecModel) View() tea.View { return r.m.View() }

// quitForHandoff stops the owned reader in a command before Bubble Tea joins
// its input loop. The model itself stays responsive during a bounded stop.
func (m *Model) quitForHandoff() tea.Cmd {
	stop := m.stopInput
	return func() tea.Msg {
		if stop != nil {
			stop()
		}
		return tea.QuitMsg{}
	}
}

func (m *Model) runPendingExec(ctx context.Context, in, out *os.File, stuck bool) (tea.Model, error) {
	pe := m.pendingExec
	m.clearHandoff()
	if stuck {
		return nil, stuckLauncherBusyError(nil)
	}
	pe.command.in, pe.command.out, pe.command.li = in, out, nil
	err := pe.command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var done tea.Msg
	if pe.after != nil {
		done = pe.after(err)
	}
	m.openBg(m.launcherContext(ctx))
	return timeFrames(&resumedExecModel{m: m, done: done}), nil
}

func (m *Model) runPendingAttach(ctx context.Context, in, out *os.File, stuck bool) (tea.Model, error) {
	pa := m.pendingAttach
	if pa == nil {
		return nil, nil
	}
	m.clearHandoff()
	if stuck {
		return nil, stuckLauncherAttachError(pa)
	}
	pa.opts.Settings, pa.settingsRead = readSettings(m.be, pa.opts.Settings, pa.settingsRead)
	pa.opts.In, pa.opts.Out = in, out
	res, err := m.be.Attach(terminalAttachContextV1(ctx), pa.opts)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if res.ReaderStuck || errors.Is(err, client.ErrAttachReaderStuck) {
		if err != nil {
			return nil, err
		}
		writeAttachEnd(os.Stderr, pa.name, pa.host, res)
		return nil, nil
	}
	m.openBg(m.launcherContext(ctx))
	return timeFrames(&resumedModel{m: m, done: attachDoneMsg{host: pa.host, id: pa.id, name: pa.name, res: res, err: err, settingsRead: pa.settingsRead}}), nil
}

// handingOff reserves one terminal transfer while its input stop is pending.
func (m *Model) handingOff() bool { return m.pendingExec != nil || m.pendingAttach != nil }

func (m *Model) transferInputBlocked() bool { return m.jacking != nil || m.handingOff() }

// clearHandoff retires both transfer intents before the next program can run.
func (m *Model) clearHandoff() {
	m.pendingExec, m.pendingAttach = nil, nil
	m.jacking = nil
	m.quitArmed = false
}
