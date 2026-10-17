package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/client"
)

// modal is a notched dialog over the launcher. Only one shows at a time.
type modal interface {
	key(m *Model, k tea.KeyPressMsg) tea.Cmd
	view(m *Model) string
}

// dialogWidth is the width of a dialog: the launcher's own column, so a
// dialog covers the list instead of cutting across it.
func (m *Model) dialogWidth() int { return m.boxW() }

// errorLines draws an error and what to try next inside a dialog.
func errorLines(err error, w int) []string {
	msg, hint := errText(err)
	return errorBlock(msg, hint, w)
}

// New session (docs/contributing/ux-spec.md 2.4).

type agentChoice struct {
	kind      string
	installed bool
}

type newSession struct {
	host     string
	choices  []agentChoice
	ai       int
	field    int // 0 agent, 1 folder, 2 name, 3 worktree, 4 command (only for "other")
	command  input
	folder   input
	name     input
	worktree bool
	suggest  []string // folder completions for the current folder text
	hints    bool     // suggest holds recent folders and repositories, not completions
	si       int      // the next suggestion Tab takes
	finding  bool     // the host's agents are not known yet
	starting bool
	err      error
}

const labelW = 11

// agentChoices lists the agents the host has, in the agent table's order,
// then the ones it lacks. The shell is always there.
func agentChoices(info *client.HostInfo) []agentChoice {
	have := map[string]bool{"shell": true}
	if info != nil {
		for k := range info.Agents {
			have[k] = true
		}
	}
	var on, off []agentChoice
	for _, a := range agents.Table {
		switch {
		case have[a.Kind]:
			on = append(on, agentChoice{a.Kind, true})
		case info != nil:
			off = append(off, agentChoice{a.Kind, false})
		}
	}
	// "other" runs any command the user types, for agents not in the table.
	on = append(on, agentChoice{otherAgent, true})
	return append(on, off...)
}

// otherAgent is the choice that runs a command the user types.
const otherAgent = "other…"

func newSessionDialog(m *Model, h *hostConn) *newSession {
	d := &newSession{
		host:    h.name(),
		choices: agentChoices(h.info),
		command: newInput("", "a command, for example aider --model sonnet"),
		folder:  newInput("~", "~"),
		name:    newInput("", "the folder name"),
		finding: h.info == nil,
	}
	// The last choice on this host, or the first agent it has.
	want := m.lastAgent[h.name()]
	for i, c := range d.choices {
		if c.installed && c.kind == want {
			d.ai = i
		}
	}
	return d
}

func (d *newSession) agent() string {
	if d.ai < len(d.choices) {
		return d.choices[d.ai].kind
	}
	return "shell"
}

func (d *newSession) busy() bool { return d.starting }

func (d *newSession) paste(s string) {
	switch d.field {
	case 1:
		d.folder.insert(s)
	case 2:
		d.name.insert(s)
	case 4:
		d.command.insert(s)
	}
}

// order is the sequence Tab moves through; the command field exists only for "other".
func (d *newSession) order() []int {
	if d.agent() == otherAgent {
		return []int{0, 4, 1, 2, 3}
	}
	return []int{0, 1, 2, 3}
}

func (d *newSession) step(n int) {
	o := d.order()
	i := 0
	for j, f := range o {
		if f == d.field {
			i = j
		}
	}
	d.field = o[(i+n+len(o))%len(o)]
}

// refresh picks up the host's agents once the doctor has answered.
func (d *newSession) refresh(m *Model) {
	h := m.hostByName(d.host)
	if !d.finding || h == nil || h.info == nil {
		return
	}
	cur := d.agent()
	d.choices, d.finding = agentChoices(h.info), false
	d.ai = 0
	want := m.lastAgent[d.host]
	for i, c := range d.choices {
		if c.installed && (c.kind == cur && want == "" || c.kind == want) {
			d.ai = i
		}
	}
}

func (d *newSession) moveAgent(step int) {
	for i := d.ai + step; i >= 0 && i < len(d.choices); i += step {
		if d.choices[i].installed {
			d.ai = i
			return
		}
	}
}

func (d *newSession) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	d.refresh(m)
	ks := k.String()
	if d.starting {
		if ks == "esc" {
			m.modal = nil
		}
		return nil
	}
	if d.field == 1 && ks == "tab" {
		// Tab completes a typed path from the host's matches, cycling through
		// them; with nothing left to complete it moves to the next field.
		if next, ok := d.completion(); ok {
			d.folder.set(next)
			return m.fetchFolders(d)
		}
	}
	switch ks {
	case "esc":
		m.modal = nil
		return nil
	case "tab", "down":
		d.step(1)
		if d.field == 1 {
			return m.fetchFolders(d)
		}
		return nil
	case "shift+tab", "up":
		d.step(-1)
		return nil
	case "enter", "ctrl+enter", "alt+enter", "ctrl+j":
		return d.submit(m, ks == "enter")
	}
	switch d.field {
	case 0:
		d.agentKey(ks, k.Text)
	case 1:
		if d.folder.key(k) {
			return m.fetchFolders(d)
		}
	case 2:
		d.name.key(k)
	case 4:
		d.command.key(k)
	case 3:
		switch ks {
		case "left", "h":
			d.worktree = false
		case "right", "l":
			d.worktree = true
		case "space":
			d.worktree = !d.worktree
		}
	}
	return nil
}

// submit starts the session the form describes; attach is false for a
// start in the background.
func (d *newSession) submit(m *Model, attach bool) tea.Cmd {
	h := m.hostByName(d.host)
	if h == nil {
		m.modal = nil
		return nil
	}
	folder := strings.TrimSpace(d.folder.value())
	if folder == "" {
		folder = "~"
	}
	a := client.NewArgs{Agent: d.agent(), Cwd: client.RemotePath(folder), Name: strings.TrimSpace(d.name.value()), Worktree: d.worktree}
	if d.agent() == otherAgent {
		c := strings.TrimSpace(d.command.value())
		if c == "" {
			d.field, d.err = 4, &client.UserError{Msg: "type the command to run", Hint: "For example: aider --model sonnet"}
			return nil
		}
		// exec replaces the shell, so the command is the session's process.
		a.Agent, a.Argv = "", []string{"/bin/sh", "-c", "exec " + c}
	}
	d.starting, d.err = true, nil
	return m.startSession(h, startReq{args: a, attach: attach, form: d})
}

// agentKey moves through the agents: arrows and h l, space, or a letter,
// which jumps to the next installed agent that starts with it.
func (d *newSession) agentKey(ks, text string) {
	switch ks {
	case "left", "h":
		d.moveAgent(-1)
	case "right", "l", "space":
		d.moveAgent(1)
	default:
		if len(text) != 1 {
			return
		}
		for i := 1; i <= len(d.choices); i++ {
			j := (d.ai + i) % len(d.choices)
			if c := d.choices[j]; c.installed && strings.HasPrefix(c.kind, text) {
				d.ai = j
				return
			}
		}
	}
}

func (d *newSession) view(m *Model) string {
	w := m.dialogWidth()
	cw := w - 4
	fw := cw - labelW
	label := func(i int, s string) string {
		w := labelW
		if i == 0 {
			w-- // the agent chips have a space on each side, so they start a cell early
		}
		if d.field == i {
			return sBold.Render(fit(s, w))
		}
		return sFaint.Render(fit(s, w))
	}
	body := []string{"", label(0, "Agent") + d.agentRow(fw+1)}
	if d.agent() == otherAgent {
		body = append(body, label(4, "Command")+d.command.view(fw, d.field == 4))
	}
	body = append(body,
		"",
		label(1, "Folder")+d.folder.view(fw, d.field == 1),
		strings.Repeat(" ", labelW)+d.suggestLine(fw),
		label(2, "Name")+d.name.view(fw, d.field == 2),
		"",
		label(3, "Worktree")+d.worktreeRow(),
		"",
	)
	switch {
	case d.starting:
		body = append(body, sKey.Render(m.spinner())+" "+sFaint.Render(fmt.Sprintf("Starting %s on %s…", d.agent(), d.host)), "")
	case d.err != nil:
		body = append(append(body, errorLines(d.err, cw)...), "")
	}
	hints := []hint{{"enter", "start"}, {"tab", "next"}, {"ctrl+enter", "start detached"}, {"esc", "cancel"}}
	return sheet(w, "New session on "+d.host, "", hints, trimBlank(body), toneNormal)
}

// worktreeRow is a choice of two: the folder itself, or a new git worktree.
func (d *newSession) worktreeRow() string {
	opt := func(on bool, s string) string {
		switch {
		case on && d.field == 3:
			return sKeyBold.Render("● " + s)
		case on:
			return sBold.Render("● " + s)
		}
		return sFaint.Render("○ " + s)
	}
	return opt(!d.worktree, "this folder") + "   " + opt(d.worktree, "new worktree")
}

// agentRow draws the agent choices. The chosen one is the selection bar
// while the row has focus, and bold when it does not.
func (d *newSession) agentRow(w int) string {
	var b strings.Builder
	used := 0
	var missing []string
	for i, c := range d.choices {
		if !c.installed {
			missing = append(missing, c.kind)
			continue
		}
		chip := " " + c.kind + " "
		n := cells(chip)
		if used+n > w {
			b.WriteString(sFaint.Render("…"))
			break
		}
		switch {
		case i == d.ai && d.field == 0:
			b.WriteString(listRow(true, seg{chip, sBold}))
		case i == d.ai:
			b.WriteString(sBold.Render(chip))
		default:
			b.WriteString(sFaint.Render(chip))
		}
		used += n
	}
	if d.finding {
		if room := w - used - 2; room > 8 {
			b.WriteString("  " + sFaint.Render(trunc("finding agents…", room)))
		}
	} else if len(missing) > 0 {
		if room := w - used - 2; room > 16 {
			b.WriteString("  " + sFaint.Render(trunc("not installed: "+strings.Join(missing, ", "), room)))
		}
	}
	return b.String()
}

// fetchFolders asks the host for completions of the folder field.
func (m *Model) fetchFolders(d *newSession) tea.Cmd {
	h := m.hostByName(d.host)
	if h == nil || !h.online() {
		return nil
	}
	prefix, name, c, ctx := d.folder.value(), h.name(), h.conn, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		res, err := c.Folders(cctx, prefix)
		if err != nil {
			return nil
		}
		return foldersMsg{host: name, prefix: prefix, res: res}
	}
}

// setSuggestions keeps completions that still fit what the user has typed.
func (d *newSession) setSuggestions(prefix string, res client.FoldersResult) {
	if prefix != d.folder.value() {
		return // the user typed on; a newer answer is coming
	}
	var out []string
	seen := map[string]bool{}
	add := func(list []string) {
		for _, s := range list {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	d.hints = prefix == "" || prefix == "~"
	if d.hints {
		add(res.Recent)
		add(res.Repos)
	} else {
		add(res.Matches)
	}
	d.suggest, d.si = out, 0
}

// suggestLine shows the completions under the folder field while it has focus.
func (d *newSession) suggestLine(w int) string {
	if d.field != 1 || len(d.suggest) == 0 {
		return ""
	}
	lead := "↹ "
	if d.hints {
		lead = "recent: "
	}
	return sFaint.Render(fit(lead+strings.Join(d.suggest, "  "), w))
}

// completion returns the next match that extends what the user typed. Adding
// only a trailing slash does not count, and the hints shown for an empty
// field (recent folders, repositories) are not taken by Tab.
func (d *newSession) completion() (string, bool) {
	v := d.folder.value()
	if v == "" || v == "~" || d.hints {
		return "", false
	}
	for range d.suggest {
		next := d.suggest[d.si%len(d.suggest)]
		d.si++
		if next != v && next != v+"/" && strings.HasPrefix(next, strings.TrimSuffix(v, "/")) {
			return next, true
		}
	}
	return "", false
}

// shellDoneMsg reports that an SSH shell on a host ended.
type shellDoneMsg struct {
	host string
	err  error
}

// openShell hands the terminal to an interactive SSH shell on the host.
func (m *Model) openShell(h *hostConn) tea.Cmd {
	s := &client.SSH{Host: h.host, Interactive: true}
	cmd, err := s.Shell(m.ctx)
	if err != nil {
		return m.toastErr(err)
	}
	name := h.name()
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return shellDoneMsg{host: name, err: err} })
}
