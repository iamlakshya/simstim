package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// The prompt line (docs/contributing/ux-spec.md 2.3) sits above the keys. "/" searches the
// sessions of every host at once; "n" starts a session from one line:
// "claude ~/src/app auth", the agent, the folder and a name, each optional,
// with Tab to complete.

type promptMode int

const (
	promptSearch promptMode = iota + 1
	promptNew
)

type promptState struct {
	mode     promptMode
	in       input
	host     string   // where a new session starts
	sel      int      // the selected search result
	folders  []string // the host's completions for the folder being typed
	asked    string   // the folder prefix last asked for
	starting bool
	err      error
	at       time.Time
}

func (m *Model) openSearch() tea.Cmd {
	m.prompt = &promptState{mode: promptSearch, in: newInput("", "a session, an agent, a folder or a host"), at: m.now()}
	return nil
}

func (m *Model) openQuickStart() tea.Cmd {
	h := m.newSessionHost()
	if h == nil {
		m.backToHosts()
		return m.toast(toastInfo, "Choose a host, then press n.")
	}
	m.prompt = &promptState{mode: promptNew, host: h.name(), in: newInput("", m.defaultAgent(h)+" ~/folder name"), at: m.now()}
	return nil
}

// agentsOn lists the agents a host has, the shell last.
func agentsOn(h *hostConn) []string {
	var out []string
	for _, c := range agentChoices(h.info) {
		if c.installed && c.kind != otherAgent {
			out = append(out, c.kind)
		}
	}
	return out
}

// defaultAgent is the agent last used on h, or its first.
func (m *Model) defaultAgent(h *hostConn) string {
	if a := m.lastAgent[h.name()]; a != "" {
		return a
	}
	if list := agentsOn(h); len(list) > 0 {
		return list[0]
	}
	return "shell"
}

// quickArgs reads the quick-start line: an agent the host has, a folder (a
// word with a slash or a tilde), "+wt" for a new worktree, and the rest as
// the name.
func quickArgs(text string, agents []string, fallback string) client.NewArgs {
	a := client.NewArgs{Agent: fallback, Cwd: "~"}
	words := strings.Fields(text)
	if len(words) > 0 {
		for _, ag := range agents {
			if words[0] == ag {
				a.Agent, words = ag, words[1:]
				break
			}
		}
	}
	var rest []string
	for _, w := range words {
		switch {
		case w == "+wt" || w == "--worktree":
			a.Worktree = true
		case a.Cwd == "~" && (strings.HasPrefix(w, "~") || strings.HasPrefix(w, "/") || strings.HasPrefix(w, ".") || strings.Contains(w, "/")):
			a.Cwd = w
		default:
			rest = append(rest, w)
		}
	}
	a.Name = strings.Join(rest, "-")
	a.Cwd = client.RemotePath(a.Cwd)
	return a
}

// ghost is what Tab would add to the word being typed: an agent's name for
// the first word, else a folder from the host.
func (m *Model) ghost() string {
	p := m.prompt
	if p == nil || p.mode != promptNew {
		return ""
	}
	text := p.in.value()
	word := text[strings.LastIndex(text, " ")+1:]
	if word == "" {
		return ""
	}
	if !strings.Contains(text, " ") {
		if h := m.hostByName(p.host); h != nil {
			for _, a := range agentsOn(h) {
				if strings.HasPrefix(a, word) && a != word {
					return a[len(word):]
				}
			}
		}
	}
	if pathWord(word) {
		for _, f := range p.folders {
			if strings.HasPrefix(f, word) && f != word && f != word+"/" {
				return f[len(word):]
			}
		}
	}
	return ""
}

// pathWord reports whether a word of the quick-start line is a folder.
func pathWord(w string) bool {
	return w != "" && (strings.ContainsAny(w[:1], "~/.") || strings.Contains(w, "/"))
}

// results are the sessions of every host that match the search, the ones
// that need you first.
func (m *Model) results() []sessRow {
	q := ""
	if m.prompt != nil {
		q = strings.TrimSpace(m.prompt.in.value())
	}
	var out []sessRow
	for _, h := range m.hosts {
		_, hostHit := fuzzy(q, h.name())
		for _, s := range h.sessions {
			if q == "" || hostHit || matchSession(q, s) {
				out = append(out, sessRow{kind: rowSession, h: h, s: s})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].s) < rank(out[j].s) })
	return out
}

func (m *Model) promptKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.prompt
	ks := k.String()
	if p.starting {
		if ks == "esc" {
			m.prompt = nil
		}
		return nil
	}
	switch ks {
	case "esc":
		m.prompt = nil
		return nil
	case "up", "ctrl+p":
		p.sel = max(p.sel-1, 0)
		return nil
	case "down", "ctrl+n":
		p.sel++
		return nil
	}
	if p.mode == promptSearch {
		rs := m.results()
		p.sel = min(p.sel, max(len(rs)-1, 0))
		if ks == "enter" {
			if p.sel >= len(rs) {
				return nil
			}
			r := rs[p.sel]
			m.prompt = nil
			m.openHost(r.h)
			m.sel, m.autoSel = r.key(), false
			m.syncSel()
			return m.attachSelected()
		}
		if p.in.key(k) {
			p.sel = 0
		}
		return nil
	}
	h := m.hostByName(p.host)
	if h == nil {
		m.prompt = nil
		return nil
	}
	switch ks {
	case "tab":
		if g := m.ghost(); g != "" {
			p.in.insert(g)
			return m.askFolders()
		}
		return nil
	case "ctrl+f":
		d := newSessionDialog(m, h)
		a := quickArgs(p.in.value(), agentsOn(h), m.defaultAgent(h))
		for i, c := range d.choices {
			if c.kind == a.Agent {
				d.ai = i
			}
		}
		d.folder.set(a.Cwd)
		d.name.set(a.Name)
		d.worktree = a.Worktree
		m.prompt, m.modal = nil, d
		return nil
	case "enter", "ctrl+enter", "alt+enter", "ctrl+j":
		a := quickArgs(p.in.value(), agentsOn(h), m.defaultAgent(h))
		p.starting, p.err = true, nil
		m.lastAgent[h.name()] = a.Agent
		return m.startSession(h, startReq{args: a, attach: ks == "enter", line: p})
	}
	if p.in.key(k) {
		p.err = nil
		return m.askFolders()
	}
	return nil
}

// askFolders asks the host for completions of the folder being typed.
func (m *Model) askFolders() tea.Cmd {
	p := m.prompt
	h := m.hostByName(p.host)
	text := p.in.value()
	word := text[strings.LastIndex(text, " ")+1:]
	if h == nil || !h.online() || !pathWord(word) || word == p.asked {
		return nil
	}
	p.asked = word
	name, c, ctx := h.name(), h.conn, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		res, err := c.Folders(cctx, word)
		if err != nil {
			return nil
		}
		return foldersMsg{host: name, prefix: word, res: res, quick: true}
	}
}

// promptLine draws the prompt: the host or "search", the text with its
// cursor and the faint completion after it.
func (m *Model) promptLine(w int) string {
	p := m.prompt
	if p == nil {
		return sFaint.Render(glyphPrompt + " / search every host" + sep + "n start a session")
	}
	label := "search"
	if p.mode == promptNew {
		label = p.host
	}
	lead := sAccent.Render(label) + " " + sAccent.Bold(true).Render(glyphPrompt) + " "
	room := w - cells(lead)
	if p.starting {
		a := quickArgs(p.in.value(), m.agentsFor(p.host), "")
		return lead + sKey.Render(m.spinner()) + " " + m.shimmer(fmt.Sprintf("Starting %s on %s…", orDefault(a.Agent, "the agent"), p.host))
	}
	g := m.ghost()
	if g == "" || p.in.pos != len(p.in.val) {
		return lead + p.in.view(room, true)
	}
	// The cursor sits on the completion's first letter.
	r := []rune(trunc(g, max(room-cells(p.in.value()), 1)))
	return lead + trunc(p.in.value(), room-len(r)) + sPlain.Reverse(true).Faint(true).Render(string(r[:1])) + sFaint.Render(string(r[1:]))
}

func (m *Model) agentsFor(host string) []string {
	if h := m.hostByName(host); h != nil {
		return agentsOn(h)
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// quickHints is the line above the prompt while a session is being started:
// the host's agents, the one the line names lit, then folders to complete.
func (m *Model) quickHints(w int) string {
	p := m.prompt
	if p == nil || p.mode != promptNew {
		return ""
	}
	h := m.hostByName(p.host)
	if h == nil {
		return ""
	}
	agents := agentsOn(h)
	a := quickArgs(p.in.value(), agents, m.defaultAgent(h))
	var parts []string
	for _, ag := range agents {
		if ag == a.Agent {
			parts = append(parts, sAccent.Bold(true).Render(ag))
		} else {
			parts = append(parts, sFaint.Render(ag))
		}
	}
	out := strings.Join(parts, "  ")
	if len(p.folders) > 0 {
		out += sFaint.Render(sep + strings.Join(p.folders, "  "))
	}
	if p.err != nil {
		msg, _ := errText(p.err)
		out = sErrHead.Render(glyphFail) + " " + sDanger.Render(sentence(msg))
	}
	return trunc(out, w)
}
