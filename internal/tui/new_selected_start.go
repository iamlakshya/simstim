package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type newSelectedReadyMsg struct {
	owner   *newSelectedStart
	attempt *client.HostAttemptV1
	err     error
}

type newSelectedResultMsg struct {
	owner   *newSelectedStart
	attempt *client.HostAttemptV1
	result  startedMsg
}

type newSelectedFinishedMsg struct {
	owner  *newSelectedStart
	host   client.Host
	result startedMsg
}

func (m *Model) startSelectedNewSession(selector string, shown hostSelectionPreview, request startReq) tea.Cmd {
	switch m.be.(type) {
	case realBackend, *realBackend:
	default:
		// Tests can use the real local metadata facade with a fake backend.
		// Display provenance never permits that fixture to contact real hosts.
		return m.newStartSourceError(request, &client.UserError{Code: "fixture_host_attempt", Kind: client.KindRefused,
			Msg: "the fixture has no real host-attempt owner", Hint: "Use the fixture's in-memory named-host start."})
	}
	ctx, cancel := context.WithCancel(m.launcherContext(m.backgroundContext()))
	owner := &newSelectedStart{ctx: ctx, cancel: cancel, selector: selector, shown: shown,
		original: request, screen: m.screen, current: m.cur}
	m.selectedNew = owner
	m.selectedOwners.add(owner)
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		end, err := owner.begin()
		if err != nil {
			return newSelectedReadyMsg{owner: owner, err: err}
		}
		defer end()
		options := startInputOptions(request.args, request.args.Cwd != "")
		owner.options = options
		selection, err := client.PrepareHostSelection(ctx, selector, options)
		if err != nil {
			return newSelectedReadyMsg{owner: owner, err: err}
		}
		owner.mu.Lock()
		owner.selection = selection
		owner.mu.Unlock()
		// Compare fresh metadata with what the user submitted. Only the actual
		// new facade below may bind fresh attempts; shown carries no authority.
		actual := hostSelectionPreview{names: selection.Names(), automatic: selection.Automatic(),
			reason: selection.Reason(), defaulted: selection.Defaulted()}
		if !actual.neutral() && len(actual.names) == 1 {
			actual.host, err = client.ResolveHost(ctx, actual.names[0])
		}
		if err == nil {
			err = selection.Check(ctx)
		}
		if err == nil && !sameHostPreview(actual, shown) {
			err = changedNewSelection()
		}
		if err != nil {
			return newSelectedReadyMsg{owner: owner, err: err}
		}
		// The UI has no --need constraints. The actual Auto owner derives
		// complete original needs from selection's settings/options and args.
		// Ordinary TUI submission never declares --allow-wake. Default/false
		// preserves the actual owner's default-N scoped paid-wake question.
		intent := request.args
		intent.Layers = nil // actual selection retained the owned original options
		plan, err := client.NewHostAttemptPlanV1(ctx, selection, intent, client.PlacementNeed{}, client.AutoPlacementOptionsV1{})
		if plan != nil {
			owner.mu.Lock()
			owner.plan, owner.selection = plan, nil // retain partial owners on error
			owner.mu.Unlock()
		}
		if err != nil {
			return newSelectedReadyMsg{owner: owner, err: err}
		}
		return m.nextSelectedAttempt(ctx, owner)
	})
}

func (m *Model) nextSelectedAttempt(ctx context.Context, owner *newSelectedStart) tea.Msg {
	call, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	attempt, err := owner.plan.Next(call, false)
	owner.setAttempt(attempt)
	err = m.continueSelectedDial(call, attempt, err)
	return newSelectedReadyMsg{owner: owner, attempt: attempt, err: err}
}

func (m *Model) continueSelectedDial(caller context.Context, attempt *client.HostAttemptV1, returned error) error {
	if returned == nil || attempt == nil {
		return returned
	}
	original := attempt.Context()
	if original == nil {
		return returned
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if until, ok := caller.Deadline(); ok {
		ctx, cancel = context.WithDeadline(original, until)
	} else {
		ctx, cancel = context.WithCancel(original)
	}
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	defer cancel()
	first := true
	return m.grantQuestions(ctx, func(ctx context.Context) error {
		if first {
			first = false
			return returned
		}
		resumed, err := attempt.Continue(ctx, false)
		if resumed != attempt {
			return changedNewSelection()
		}
		return err
	})
}

func (m *Model) onSelectedReady(msg newSelectedReadyMsg) tea.Cmd {
	owner := msg.owner
	if !m.selectedNewLive(owner) {
		return m.releaseSelectedNew(owner)
	}
	if msg.attempt != owner.currentAttempt() {
		return nil // an older attempt cannot end or replace its successor
	}
	if msg.err != nil {
		if owner.currentAttempt() != nil {
			return m.retrySelectedNew(owner, startedMsg{startReq: owner.original, err: msg.err}, msg.err)
		}
		return m.finishSelectedNew(owner, startedMsg{startReq: owner.original, err: msg.err})
	}
	attempt := owner.currentAttempt()
	actual, conn, request := attempt.Host(), attempt.Conn(), attempt.Request()
	if conn == nil || request == nil || attempt.Context() == nil || !client.ValidHostName(actual.Name) {
		return m.finishSelectedNew(owner, startedMsg{startReq: owner.original, err: changedNewSelection()})
	}
	r := owner.original
	r.host, r.args, r.grants, r.wallIntent, r.selected = actual.Name, attempt.Args(), request, r.args.Wall, owner
	r.selectedHost = actual
	r.inputOptions = &owner.options
	// The hostConn is private to this attempt, never the highlighted host or
	// a reused launcher bridge. The concrete owner remains its sole closer.
	host := &hostConn{host: actual, conn: cleanConn{conn}, status: stOnline}
	return m.runSelectedNew(owner, m.startSessionWithSource(host, r))
}

func (m *Model) runSelectedNew(owner *newSelectedStart, command tea.Cmd) tea.Cmd {
	attempt := owner.currentAttempt()
	return func() tea.Msg {
		end, err := owner.begin()
		if err != nil {
			return newSelectedResultMsg{owner: owner, attempt: attempt, result: startedMsg{startReq: owner.original, err: err}}
		}
		defer end()
		if err := attempt.CheckReadyV1(owner.ctx); err != nil {
			return newSelectedResultMsg{owner: owner, attempt: attempt, result: startedMsg{startReq: owner.original, err: err}}
		}
		message := command()
		if message == nil {
			return nil
		}
		result, ok := message.(startedMsg)
		if !ok {
			return message
		}
		return newSelectedResultMsg{owner: owner, attempt: attempt, result: result}
	}
}

func (m *Model) onSelectedResult(msg newSelectedResultMsg) tea.Cmd {
	if msg.attempt != msg.owner.currentAttempt() {
		return nil
	}
	if keepStartGrant(msg.result) && m.selectedNewLive(msg.owner) {
		return m.onStarted(msg.result) // same actual Conn/R remains for the answer
	}
	if msg.result.err != nil && msg.result.s == nil && msg.result.selectedNewCalled && m.selectedNewLive(msg.owner) {
		return m.retrySelectedNew(msg.owner, msg.result, msg.result.err)
	}
	return m.finishSelectedNew(msg.owner, msg.result)
}

func (m *Model) retrySelectedNew(owner *newSelectedStart, result startedMsg, actual error) tea.Cmd {
	attempt := owner.currentAttempt()
	return m.bgCmdFrom(owner.ctx, func(ctx context.Context) tea.Msg {
		end, err := owner.begin()
		if err != nil {
			result.cleanupErr = errors.Join(result.cleanupErr, err)
			return newSelectedResultMsg{owner: owner, attempt: attempt, result: result}
		}
		defer end()
		again, err := owner.plan.Retry(ctx, attempt, actual)
		if err == nil && again {
			return m.nextSelectedAttempt(ctx, owner)
		}
		result.selectedNewCalled = false // the exact result has been adjudicated
		result.cleanupErr = errors.Join(result.cleanupErr, err)
		return newSelectedResultMsg{owner: owner, attempt: attempt, result: result}
	})
}

func (m *Model) finishSelectedNew(owner *newSelectedStart, result startedMsg) tea.Cmd {
	return m.bgCmd(func(context.Context) tea.Msg {
		var actual client.Host
		if owner.currentAttempt() != nil {
			actual = owner.currentAttempt().Host()
		}
		result.cleanupErr = errors.Join(result.cleanupErr, m.selectedOwners.release(owner))
		return newSelectedFinishedMsg{owner: owner, host: actual, result: result}
	})
}

func (m *Model) onSelectedFinished(msg newSelectedFinishedMsg) tea.Cmd {
	if m.selectedNew == msg.owner {
		m.selectedNew = nil
	} else {
		// The same form may already be waiting for a newer logical start.
		msg.result.form, msg.result.line = nil, nil
	}
	if msg.result.s != nil {
		msg.result.selectedHost = msg.host
		host := m.hostByName(msg.host.Name)
		if host == nil || !client.SameEntry(host.host, msg.host) {
			// The remote start succeeded. A changed saved target must never
			// acquire that session row or become its attach destination.
			commands := []tea.Cmd{m.newStartSourceError(msg.result.startReq, changedNewSelection()),
				m.toast(toastSuccess, "Started "+shown(msg.result.s.Name)+" on "+shown(msg.host.Name)+".")}
			if msg.result.cleanupErr != nil {
				commands = append(commands, m.toastErr(msg.result.cleanupErr))
			}
			return tea.Batch(commands...)
		}
	}
	return m.onStarted(msg.result)
}

func changedNewSelection() error {
	return &client.UserError{Code: "host_changed", Kind: client.KindRefused,
		Msg: "the host selection changed before the new start", Hint: "Choose the host again."}
}
