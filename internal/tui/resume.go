package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// The resume of a session whose agent has ended (docs/contributing/ux-spec.md
// 2.4.1). The host starts the agent again in the same session. The launcher
// then attaches, as after a start.

// resumedMsg is how a resume went.
type resumedMsg struct {
	key sessKey
	res *client.ResumeResult
	err error
	settingsRead
}

// resumeSelected starts the selected session's agent again, with the cap
// it asked for, and the secrets it started with, fetched again. While a
// resume of the session runs, the key does nothing. The row still says
// that the agent ended until the host has stopped the old holder.
func (m *Model) resumeSelected() tea.Cmd {
	if h, s := m.selectedPair(); h != nil && s != nil {
		return m.withHostTier(h, m.resumeSelectedReady)
	}
	return nil
}

func (m *Model) resumeSelectedReady() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		return nil
	}
	key := sessKey{h.name(), s.ID}
	if m.resuming[key] {
		return nil
	}
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	if m.resuming == nil {
		m.resuming = map[sessKey]bool{}
	}
	m.resuming[key] = true
	c, be, originalHost, originalSession, kept := h.conn, m.be, h.host, *s, m.settings
	request := client.NewGrantRequest(false, false)
	m.grantOwners.add(request)
	read := m.beginSettingsRead()
	resume := m.bgCmd(func(ctx context.Context) (message tea.Msg) {
		defer func() {
			if result, ok := message.(resumedMsg); ok {
				result.err = errors.Join(result.err, m.grantOwners.release(ctx, request))
				message = result
			}
		}()
		settings, read := readSettings(be, kept, read)
		if _, err := client.ReadPolicySettings(); err != nil {
			return resumedMsg{key: key, err: err, settingsRead: read}
		}
		// The host stops the old holder and its unit, then waits for the start.
		cctx, cancel := context.WithTimeout(client.WithGrantRequest(ctx, request), 90*time.Second)
		defer cancel()
		endAttempt, err := m.grantOwners.begin(request, cctx, cancel)
		if err != nil {
			return resumedMsg{key: key, err: err, settingsRead: read}
		}
		defer endAttempt()
		var plan client.ResumePolicyPlan
		if planner, ok := unclean(c).(interface {
			PlanResumePolicy(context.Context, string) (client.ResumePolicyPlan, error)
		}); ok {
			var err error
			plan, err = planner.PlanResumePolicy(cctx, key.id)
			if err != nil {
				return resumedMsg{key: key, err: err, settingsRead: read}
			}
		}
		effectiveNames, err := client.ResumePlanSecretNames(originalHost, originalSession, nil, plan)
		if err != nil {
			return resumedMsg{key: key, err: err, settingsRead: read}
		}
		if err := m.grantQuestions(cctx, func(ctx context.Context) error {
			return c.CheckResumeGrants(ctx, key.id, effectiveNames)
		}); err != nil {
			return resumedMsg{key: key, err: err, settingsRead: read}
		}
		if planner, ok := unclean(c).(interface {
			CheckResumePolicyBeforeFetch(context.Context, string, client.ResumePolicyPlan) error
		}); ok {
			if err := m.grantQuestions(cctx, func(ctx context.Context) error {
				return planner.CheckResumePolicyBeforeFetch(ctx, key.id, plan)
			}); err != nil {
				return resumedMsg{key: key, err: err, settingsRead: read}
			}
		}
		var secrets map[string]api.Secret
		err = m.grantQuestions(cctx, func(ctx context.Context) error {
			var err error
			secrets, err = be.SessionSecrets(ctx, settings, originalHost, key.id, effectiveNames)
			return err
		})
		if err != nil {
			return resumedMsg{key: key, err: err, settingsRead: read}
		}
		var res *client.ResumeResult
		err = m.grantQuestions(cctx, func(ctx context.Context) error {
			var err error
			res, err = c.Resume(ctx, client.ResumeArgs{ID: key.id, Secrets: secrets, Layers: plan.Layers})
			return err
		})
		return resumedMsg{key: key, res: res, err: err, settingsRead: read}
	})
	name, host := shown(s.Name), shown(h.name())
	text := fmt.Sprintf("Resuming %s on %s…", name, host)
	if client.CapStopped(*s) && s.MemoryMax > 0 {
		// The cap that stopped the agent applies again, unless the command
		// line gives another.
		text = fmt.Sprintf("Resuming %s on %s, with its cap of %s…", name, host, fmtSize(s.MemoryMax))
	}
	return tea.Batch(m.toast(toastInfo, text), resume)
}

// onResumed shows the session's new row, and attaches to it while it is
// still the one selected and nothing else is open. Otherwise a notice says
// that it runs again.
func (m *Model) onResumed(msg resumedMsg) tea.Cmd {
	delete(m.resuming, msg.key)
	if msg.err != nil {
		return m.toastErr(msg.err)
	}
	h := m.hostByName(msg.key.host)
	if h == nil || msg.res == nil {
		return nil
	}
	s := msg.res.Session
	for i := range h.sessions {
		if h.sessions[i].ID == s.ID {
			h.sessions[i] = s
		}
	}
	h.changes++
	// The new holder's output starts again from its first byte, so what the
	// terminal kept of the old one cannot be replayed.
	if m.attach != nil && m.attach.Key == h.name()+"/"+s.ID {
		m.attach.Valid = false
	}
	if m.sel == msg.key && m.modal == nil && m.prompt == nil && m.rename == nil {
		return m.jack(h, s.ID, s.Name)
	}
	return tea.Batch(m.toast(toastSuccess, fmt.Sprintf("Resumed %s on %s.", shown(s.Name), shown(h.name()))), m.listSessions(h))
}
