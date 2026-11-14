package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/egress"
)

// policyConn is the ordinary policy transport. Older backends fail visibly;
// they never supply a made-up policy or pretend a grant was sent.
type policyConn interface {
	Policy(context.Context, string) (api.Policy, error)
	AllowPolicy(context.Context, api.PolicyAddArgs) (api.Rule, error)
	RemovePolicy(context.Context, api.PolicyRmArgs) (int, error)
	PolicyLog(context.Context, api.PolicyLogArgs) ([]client.PolicyLogEntry, error)
}

type policySheet struct {
	seq            uint64
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	origin         client.Host
	gen            int
	host, id, name string
	level          string
	policy         api.Policy
	entries        []client.PolicyLogEntry
	sel            int
	loading        bool
	adding         bool
	log            bool
	target         input
	err            error
}

type policyMsg struct {
	seq     uint64
	owner   *policySheet
	policy  api.Policy
	entries []client.PolicyLogEntry
	notice  string
	err     error
}

func (d *policySheet) paste(text string) {
	if !d.closed && d.adding && !d.loading {
		d.target.insert(text)
	}
}

func policyUnavailable() error {
	return &client.UserError{Msg: "this host helper cannot read or change sandbox policy",
		Hint: "Update the host helper, then open the policy again.", Code: "helper_old"}
}

func (m *Model) openPolicy() tea.Cmd { return m.openSessionPolicy(false) }

func (m *Model) openPolicyLog() tea.Cmd { return m.openSessionPolicy(true) }

func (m *Model) openSessionPolicy(log bool) tea.Cmd {
	h, s := m.selectedPair()
	if h == nil || s == nil {
		return nil
	}
	if s.Wall == nil || s.Wall.Level == "none" {
		return m.toastErr(&client.UserError{Msg: "this session has no sandbox policy",
			Hint: "Start a walled session, then open its policy.", Code: "not_walled"})
	}
	cleanup := m.disposeModal()
	d := &policySheet{host: h.name(), id: s.ID, name: s.Name, level: s.Wall.Level, log: log,
		origin: h.host, gen: h.gen, target: newInput("", "api.example.com")}
	m.modal = d
	return tea.Batch(cleanup, m.readPolicy(d))
}

func (m *Model) readPolicy(d *policySheet) tea.Cmd {
	if m.modal != d || d.closed {
		return nil
	}
	c, err := m.policyConnection(d)
	if err != nil {
		d.err = err
		return nil
	}
	ctx, seq := d.begin(m)
	log, id := d.log, d.id
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return policyMsg{owner: d, seq: seq, err: err}
		}
		if log {
			entries, err := c.PolicyLog(ctx, api.PolicyLogArgs{ID: id, Door: api.DoorNet, Max: 100})
			return policyMsg{owner: d, seq: seq, entries: entries, err: err}
		}
		policy, err := c.Policy(ctx, id)
		return policyMsg{owner: d, seq: seq, policy: policy, err: err}
	})
}

func (m *Model) onPolicy(msg policyMsg) tea.Cmd {
	d := msg.owner
	if policyOwner(m.modal) != d || d == nil || d.closed || d.seq != msg.seq || d.ctx == nil || d.ctx.Err() != nil {
		return nil
	}
	if _, err := m.policyConnection(d); err != nil {
		d.interrupted(err)
		return nil
	}
	d.finishOp()
	d.loading, d.err = false, msg.err
	if msg.err != nil {
		return nil
	}
	d.policy, d.entries = msg.policy, msg.entries
	d.sel = min(d.sel, max(len(d.policy.Rules)-1, 0))
	if msg.notice != "" {
		d.adding = false
		return tea.Batch(m.toast(toastSuccess, msg.notice), m.readPolicy(d))
	}
	return nil
}

func (d *policySheet) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if m.modal != d || d.closed || m.transferInputBlocked() {
		return nil
	}
	ks := k.String()
	if ks == "esc" {
		if d.adding {
			d.adding = false
		} else {
			d.stop()
			m.modal = nil
		}
		return nil
	}
	if d.loading {
		return nil
	}
	if d.adding {
		if ks == "enter" || ks == "ctrl+o" {
			return m.allowPolicy(d, ks == "ctrl+o")
		}
		d.target.key(k)
		return nil
	}
	switch ks {
	case "q":
		d.stop()
		m.modal = nil
	case "a":
		if !d.log {
			d.adding, d.err = true, nil
		}
	case "up", "k":
		d.sel = max(d.sel-1, 0)
	case "down", "j":
		d.sel = min(d.sel+1, max(len(d.policy.Rules)-1, 0))
	case "delete", "backspace":
		return m.removePolicy(d)
	case "enter", "o":
		if !d.log && len(d.policy.Rules) > 0 {
			d.target.set(d.policy.Rules[d.sel].Target)
			return m.allowPolicy(d, ks == "o")
		}
	case "l":
		d.log = !d.log
		return m.readPolicy(d)
	case "r":
		return m.readPolicy(d)
	}
	return nil
}

func (m *Model) policyChange(d *policySheet, run func(context.Context, policyConn) (string, error)) tea.Cmd {
	if m.modal != d || d.closed || d.loading || m.transferInputBlocked() {
		return nil
	}
	c, err := m.policyConnection(d)
	if err != nil {
		d.err = err
		return nil
	}
	ctx, seq := d.begin(m)
	request := client.NewGrantRequest(false, false)
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		ctx, cancel := context.WithTimeout(client.WithGrantRequest(ctx, request), 30*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return policyMsg{owner: d, seq: seq, err: err}
		}
		var text string
		err := m.grantQuestions(ctx, func(ctx context.Context) error {
			var err error
			text, err = run(ctx, c)
			return err
		})
		return policyMsg{owner: d, seq: seq, notice: text, err: err}
	})
}

func (m *Model) allowPolicy(d *policySheet, once bool) tea.Cmd {
	target := strings.TrimSpace(d.target.value())
	if target == "" {
		d.err = &client.UserError{Msg: "type the network target to allow", Hint: "Type a host such as api.example.com.", Code: "missing_value", Kind: client.KindUsage}
		return nil
	}
	id, name := d.id, d.name
	return m.policyChange(d, func(ctx context.Context, c policyConn) (string, error) {
		r, err := c.AllowPolicy(ctx, api.PolicyAddArgs{ID: id, Allow: true, Door: api.DoorNet, Target: target, Once: once})
		when := "for this session"
		if r.Once {
			when = "for one matching flow"
		} else if !r.Until.IsZero() {
			when = "until " + r.Until.UTC().Format(time.RFC3339)
		}
		return fmt.Sprintf("Granted %s to %s %s.", shown(r.Target), shown(name), when), err
	})
}

func (m *Model) removePolicy(d *policySheet) tea.Cmd {
	if d.log || len(d.policy.Rules) == 0 {
		return nil
	}
	r := d.policy.Rules[d.sel]
	if r.Layer != api.LayerSession {
		d.err = &client.UserError{Msg: "this rule belongs to another policy layer",
			Hint: "Only session rules can be removed here.", Code: "policy_layer", Kind: client.KindUsage}
		return nil
	}
	found, err := egress.CheckRuleIDSelection(d.policy.Rules, r.ID, api.LayerRef{Kind: api.LayerSession})
	if err != nil {
		d.err = client.PolicyError(err)
		return nil
	}
	if !found {
		d.err = &client.UserError{Msg: "the selected session rule is no longer in this policy", Hint: "Refresh the policy, then select the session rule again.", Code: "not_found", Kind: client.KindNotFound}
		return nil
	}
	id, name := d.id, d.name
	return m.policyChange(d, func(ctx context.Context, c policyConn) (string, error) {
		_, err := c.RemovePolicy(ctx, api.PolicyRmArgs{Session: id, ID: r.ID})
		return fmt.Sprintf("Revoked %s from %s.", shown(r.Target), shown(name)), err
	})
}

func (d *policySheet) view(m *Model) string {
	w := m.dialogWidth()
	cw := w - 4
	body := []string{"", "Wall: " + shown(d.level) + dot + "Preset: " + shown(string(d.policy.Preset)) + dot + "Unknown: " + shown(string(d.policy.Unknown)), ""}
	if d.log {
		room := max(m.height-12, 1)
		for _, e := range d.entries[max(len(d.entries)-room, 0):] {
			body = append(body, trunc(shown(e.T)+"  "+shown(e.Decision)+" "+shown(e.Host)+"  "+shown(e.Rule), cw))
		}
	} else {
		room := max((m.height-14)/2, 1)
		top := max(d.sel-room+1, 0)
		for i := top; i < len(d.policy.Rules) && i < top+room; i++ {
			r := d.policy.Rules[i]
			action := "deny"
			if r.Allow {
				action = "allow"
			}
			text := shown(r.ID) + " " + action + " " + shown(string(r.Door)) + " " + shown(r.Target)
			end := "this session"
			if r.Once {
				end = "once"
			} else if !r.Until.IsZero() {
				end = r.Until.UTC().Format(time.RFC3339)
			}
			body = append(body, marker(i == d.sel)+trunc(text, max(cw-2, 0)), "  "+sFaint.Render(trunc(shown(string(r.Layer))+" "+shown(r.Name)+policySourceLabel(r.Source)+dot+end, max(cw-2, 0))))
		}
		if len(d.policy.Locks) > 0 {
			body = append(body, sFaint.Render(fmt.Sprintf("%d policy locks apply.", len(d.policy.Locks))))
		}
		if len(d.policy.Rules) == 0 && !d.loading {
			body = append(body, sFaint.Render("No extra rules. The preset and agent rules still apply."))
		}
		if d.adding {
			body = append(body, "", "Allow net  "+d.target.view(max(cw-11, 0), true))
		}
	}
	if d.loading {
		body = append(body, "", m.spinner()+" Reading policy…")
	}
	if d.err != nil {
		body = append(append(body, ""), errorLines(d.err, cw)...)
	}
	hints := []hint{{"a", "add"}, {"enter", "allow"}, {"o", "once"}, {"del", "remove"}, {"l", "log"}, {"esc", "back"}}
	if d.adding {
		hints = []hint{{"enter", "allow for session"}, {"ctrl+o", "allow once"}, {"esc", "back"}}
	} else if d.log {
		hints = []hint{{"r", "refresh"}, {"l", "rules"}, {"esc", "back"}}
	}
	return sheet(w, "Policy of "+shown(d.name), "", hints, trimBlank(body), toneNormal)
}

type policyStoppedMsg struct {
	host client.Host
	gen  int
	ctx  context.Context
	name string
	err  error
}

func (m *Model) stopSelected() tea.Cmd {
	h, s := m.selectedPair()
	if h == nil || s == nil || !h.online() {
		return nil
	}
	stopper, ok := unclean(h.conn).(interface {
		Stop(context.Context, string) (*client.Session, error)
	})
	if !ok {
		return m.toastErr(&client.UserError{Msg: "this host helper cannot stop sandboxes",
			Hint: "Update the host helper, then try again.", Code: "helper_old"})
	}
	ctx, host, gen, id, name := m.backgroundContext(), h.host, h.gen, s.ID, s.Name
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := cctx.Err(); err != nil {
			return policyStoppedMsg{host: host, gen: gen, ctx: ctx, name: name, err: err}
		}
		_, err := stopper.Stop(cctx, id)
		return policyStoppedMsg{host: host, gen: gen, ctx: ctx, name: name, err: err}
	})
}

func (m *Model) onPolicyStopped(msg policyStoppedMsg) tea.Cmd {
	h := m.hostByName(msg.host.Name)
	if msg.ctx == nil || msg.ctx.Err() != nil || h == nil || h.gen != msg.gen || !client.SameEntry(h.host, msg.host) {
		return nil
	}
	return m.onOpDone(opDoneMsg{host: msg.host.Name, err: msg.err,
		ok: fmt.Sprintf("Stopped %s on %s. Its work stays.", shown(msg.name), shown(msg.host.Name))})
}

func policySourceLabel(source api.LayerSource) string {
	if source == api.SourceRepositoryFile {
		return " (repository file)"
	}
	return ""
}
