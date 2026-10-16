package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/version"
)

// scope is where a key binding works.
type scope int

const (
	anywhere scope = iota
	onHosts
	onSessions
)

type binding struct {
	key   string
	where scope
}

// command is one entry of the registry. The same registry feeds the
// palette, the shortcuts and the keys of the launcher (docs/contributing/ux-spec.md 2.7).
type command struct {
	noun, verb string
	short      string // the words in the shortcuts, if not the verb
	keys       []binding
	firstRun   bool // works on the first-run screen too
	hidden     bool // kept out of the palette
	why        func(m *Model) string
	run        func(m *Model) tea.Cmd
	// instead, if set, is what the command's key does when the command
	// cannot run, in place of the notice that says why. It reports whether
	// it took the key.
	instead func(m *Model) (tea.Cmd, bool)
}

func (c *command) name() string { return c.noun + ": " + c.verb }

// keyLabel is the first key, as the palette and the shortcuts show it.
func (c *command) keyLabel() string {
	if len(c.keys) == 0 {
		return ""
	}
	return keyName(c.keys[0].key)
}

// keyName is how a key is written on screen: lower case, as typed:
// "enter", "ctrl+o", "opt+e", "n".
func keyName(k string) string {
	return strings.Replace(k, "alt+", optKey+"+", 1)
}

// label is a command as the palette says it: "New session", "Add host".
func (c *command) label() string {
	s := c.short
	if s == "" {
		s = c.verb
		if (c.noun == "session" || c.noun == "host") && !strings.Contains(c.verb, " ") {
			s = c.verb + " " + c.noun
		}
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// reason is why the command cannot run now, or "".
func (c *command) reason(m *Model) string {
	if len(m.hosts) == 0 && !c.firstRun {
		return "add a host first"
	}
	if c.why == nil {
		return ""
	}
	return c.why(m)
}

func (c *command) applies(m *Model, b binding) bool {
	switch b.where {
	case onHosts:
		return m.screen == scrHosts
	case onSessions:
		return m.screen == scrSessions
	default:
		// The command works anywhere.
	}
	return true
}

func (c *command) matches(m *Model, key string) bool {
	if len(m.hosts) == 0 && !c.firstRun {
		return false
	}
	for _, b := range c.keys {
		if b.key == key && c.applies(m, b) {
			return true
		}
	}
	return false
}

func (m *Model) runCommand(c *command) tea.Cmd {
	if r := c.reason(m); r != "" {
		if c.instead != nil {
			if cmd, ok := c.instead(m); ok {
				return cmd
			}
		}
		return m.toast(toastInfo, sentence("Not available: "+r))
	}
	return c.run(m)
}

// commands is the registry. It is built in init, because its commands open
// the palette, which reads the registry.
var commands []*command

func init() { commands = registry() }

func registry() []*command {
	needSession := func(m *Model) string {
		if m.selected() == nil {
			return "no session selected"
		}
		return ""
	}
	needHost := func(m *Model) string {
		if m.selectedHost() == nil {
			return "choose a host first"
		}
		return ""
	}
	// needAgent is for what needs the agent to run, and needScreen for what
	// needs only the session's holder, which keeps the last screen.
	needAgent := func(m *Model) string {
		if s := m.selected(); s != nil && ended(*s) {
			return agentEnded
		}
		return needSession(m)
	}
	needEnded := func(m *Model) string {
		if s := m.selected(); s != nil && !ended(*s) {
			return "the agent still runs"
		}
		return needSession(m)
	}
	needScreen := func(m *Model) string {
		if s := m.selected(); s != nil && !s.Alive {
			return "the session's holder has stopped"
		}
		return needSession(m)
	}
	return []*command{
		{noun: "session", verb: "new", short: "new session", keys: []binding{{"n", anywhere}},
			why: func(m *Model) string {
				h := m.newSessionHost()
				switch {
				case h == nil:
					return "choose a host first"
				case h.status == stOffline || h.status == stFailed:
					return h.name() + " is offline"
				}
				return ""
			},
			run: (*Model).openQuickStart},
		{noun: "session", verb: "new with the form", short: "new session, with the form", why: func(m *Model) string {
			if m.newSessionHost() == nil {
				return "choose a host first"
			}
			return ""
		}, run: (*Model).openNewSession},
		{noun: "session", verb: "attach", keys: []binding{{"enter", onSessions}},
			why: func(m *Model) string {
				if m.screen == scrSessions && m.selected() == nil && sessionRows(m.rows()) == 0 {
					return ""
				}
				return needAgent(m)
			},
			run: (*Model).attachSelected, instead: (*Model).enterEnded},
		{noun: "session", verb: "preview", short: "peek at the screen", keys: []binding{{"space", onSessions}}, why: needScreen, run: (*Model).openPreview},
		{noun: "session", verb: "rename", keys: []binding{{"r", onSessions}, {"f2", onSessions}}, why: needSession, run: (*Model).startRename},
		{noun: "session", verb: "delete", keys: []binding{{"d", onSessions}}, why: needSession, run: (*Model).confirmDelete},
		{noun: "session", verb: "resume", keys: []binding{{resumeKey, onSessions}}, why: needEnded, run: (*Model).resumeSelected},
		{noun: "session", verb: "copy attach command", short: "copy attach command", keys: []binding{{"y", onSessions}}, why: needAgent, run: (*Model).copyAttach},
		{noun: "session", verb: "next waiting", short: "next waiting session", keys: []binding{{"w", anywhere}}, run: (*Model).nextWaiting},

		{noun: "host", verb: "open", short: "open host", keys: []binding{{"enter", onHosts}},
			why: func(m *Model) string {
				if m.screen != scrHosts {
					return "the sessions are open"
				}
				return needHost(m)
			},
			run: (*Model).openSelectedHost},
		{noun: "host", verb: "add", short: "add host", keys: []binding{{"a", anywhere}}, firstRun: true, run: (*Model).openWizard},
		{noun: "host", verb: "set up", short: "guided setup", keys: []binding{{"s", anywhere}}, firstRun: true, run: (*Model).openGuide},
		{noun: "host", verb: "details", short: "host details", keys: []binding{{"i", anywhere}}, why: needHost,
			run: func(m *Model) tea.Cmd { m.modal = &details{host: m.selectedHost().name()}; return nil }},
		{noun: "host", verb: "test", why: needHost, run: func(m *Model) tea.Cmd { return m.testHost(m.selectedHost()) }},
		{noun: "host", verb: "reconnect", why: needHost, run: func(m *Model) tea.Cmd { return m.reconnect(m.selectedHost()) }},
		{noun: "host", verb: "remove", why: needHost,
			run: func(m *Model) tea.Cmd { m.modal = removeHostDialog(m.selectedHost().host); return nil }},

		{noun: "view", verb: "hosts", short: "back to the hosts", why: func(m *Model) string {
			if m.screen == scrHosts {
				return "the hosts are open"
			}
			return ""
		}, run: func(m *Model) tea.Cmd { m.backToHosts(); return nil }},
		{noun: "view", verb: "all sessions", run: func(m *Model) tea.Cmd { m.openHost(nil); return nil }},
		{noun: "view", verb: "search", short: "search every host", keys: []binding{{"/", anywhere}}, run: (*Model).openSearch},
		{noun: "view", verb: "toggle ended", short: "show ended", keys: []binding{{"alt+e", onSessions}},
			run: func(m *Model) tea.Cmd { m.hideExited = !m.hideExited; m.syncSel(); return nil }},
		{noun: "view", verb: "shortcuts", short: "keys", keys: []binding{{"?", anywhere}}, firstRun: true,
			run: func(m *Model) tea.Cmd { m.modal = &helpSheet{}; return nil }},
		{noun: "view", verb: "commands", keys: []binding{{"ctrl+o", anywhere}}, firstRun: true, hidden: true,
			run: func(m *Model) tea.Cmd { m.modal = &palette{in: newInput("", "")}; return nil }},

		{noun: brand.Name, verb: "settings", firstRun: true, run: (*Model).openSettings},
		{noun: brand.Name, verb: "about", firstRun: true,
			run: func(m *Model) tea.Cmd {
				return m.toast(toastInfo, fmt.Sprintf("%s %s, protocol %s, host helper %s.", brand.DisplayName, version.Version, version.Protocol, m.be.HelperVersion()))
			}},
		{noun: brand.Name, verb: "quit", keys: []binding{{"q", anywhere}}, firstRun: true,
			run: func(*Model) tea.Cmd { return tea.Quit }},
	}
}

// Palette (docs/contributing/ux-spec.md 2.7): Ctrl+O, a filter line, then "noun  verb" rows
// with the keys on the right.

type palette struct {
	in  input
	sel int
	top int
}

type paletteItem struct {
	c      *command
	reason string
}

func (p *palette) items(m *Model) []paletteItem {
	type scored struct {
		paletteItem
		score, order int
	}
	var list []scored
	for i, c := range commands {
		if c.hidden {
			continue
		}
		s, ok := fuzzy(p.in.value(), c.name())
		if !ok {
			continue
		}
		list = append(list, scored{paletteItem{c, c.reason(m)}, s, i})
	}
	if strings.TrimSpace(p.in.value()) != "" {
		sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	}
	out := make([]paletteItem, len(list))
	for i, s := range list {
		out[i] = s.paletteItem
	}
	return out
}

func (p *palette) paste(s string) { p.in.insert(s); p.sel = 0 }

func (p *palette) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	items := p.items(m)
	switch k.String() {
	case "esc", "ctrl+o":
		m.modal = nil
	case "up", "ctrl+p":
		p.sel = max(p.sel-1, 0)
	case "down", "ctrl+n":
		p.sel = min(p.sel+1, max(len(items)-1, 0))
	case "enter":
		if p.sel >= len(items) {
			return nil
		}
		it := items[p.sel]
		if it.reason != "" {
			return m.toast(toastInfo, sentence("Not available: "+it.reason))
		}
		m.modal = nil
		return it.c.run(m)
	default:
		if p.in.key(k) {
			p.sel, p.top = 0, 0
		}
	}
	return nil
}

func (p *palette) view(m *Model) string {
	w := m.dialogWidth()
	cw := w - 4
	items := p.items(m)
	listH := max(min(len(items), m.height-12), 1)
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+listH {
		p.top = p.sel - listH + 1
	}
	body := []string{sAccent.Bold(true).Render(glyphPrompt) + " " + p.in.view(cw-2, true), ""}
	for i := p.top; i < len(items) && i < p.top+listH; i++ {
		it := items[i]
		label, key := seg{it.c.label(), sBold}, it.c.keyLabel()
		if it.reason != "" {
			label = seg{it.c.label() + sFaint.Render(dot+it.reason), sFaint}
		}
		labelW := cw - 2 - cells(key) - 2
		body = append(body, marker(i == p.sel)+listRow(i == p.sel, seg{fit(label.s, labelW), label.st}, seg{"  ", sPlain}, seg{key, sKeyBold}))
	}
	if len(items) == 0 {
		body = append(body, sFaint.Render(trunc(fmt.Sprintf("Nothing matches '%s'.", p.in.value()), cw)))
	}
	return sheet(w, "Commands", "", []hint{{"enter", "run"}, {"↑↓", "move"}, {"esc", "close"}}, body, toneNormal)
}

// Keys (docs/contributing/ux-spec.md 2.7): ? opens a sheet of every key that works here,
// built from the live registry, so a rebound key shows its new name.

type helpSheet struct{}

func (h *helpSheet) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "?", "q", "enter":
		m.modal = nil
	}
	return nil
}

func (h *helpSheet) view(m *Model) string {
	w := m.dialogWidth()
	return sheet(w, "Keys", "", []hint{{"esc", "close"}}, m.helpLines(w-4), toneNormal)
}

// helpItems are the keys that work on this screen.
func (m *Model) helpItems() [][2]string {
	var items [][2]string
	if len(m.hosts) > 0 {
		items = append(items, [2]string{"↑↓ jk", "move"}, [2]string{"1–9", "open that row"})
		if m.screen == scrSessions {
			items = append(items, [2]string{"esc", "back to hosts"})
		}
	}
	for _, c := range commands {
		// The sheet leaves out its own key: it is open already.
		if c.hidden || len(m.hosts) == 0 && !c.firstRun || c.name() == "view: shortcuts" {
			continue
		}
		var keys []string
		for _, b := range c.keys {
			if c.applies(m, b) && !slices.Contains(keys, keyName(b.key)) {
				keys = append(keys, keyName(b.key))
			}
		}
		if len(keys) == 0 {
			continue
		}
		words := c.short
		if words == "" {
			words = c.verb
		}
		items = append(items, [2]string{strings.Join(keys, " "), words})
	}
	return append(items, [2]string{"ctrl+o", "commands"}, [2]string{"ctrl+c ctrl+c", "quit"})
}

// helpLines lays the keys out in two columns, or one when narrow.
func (m *Model) helpLines(cw int) []string {
	items := m.helpItems()
	keyW := 0
	for _, it := range items {
		keyW = max(keyW, cells(it[0]))
	}
	keyW = min(keyW, 13)
	cell := func(it [2]string, w int) string {
		return sKey.Render(fit(it[0], keyW)) + "  " + sFaint.Render(trunc(it[1], max(w-keyW-2, 1)))
	}
	if cw < 64 {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = cell(it, cw)
		}
		return out
	}
	colW := cw / 2
	half := (len(items) + 1) / 2
	var out []string
	for i := range half {
		l := fit(cell(items[i], colW-2), colW)
		if i+half < len(items) {
			l += cell(items[i+half], cw-colW)
		}
		out = append(out, l)
	}
	return out
}

// Settings open in $EDITOR.

type editorDoneMsg struct{ err error }

func (m *Model) openSettings() tea.Cmd {
	path := filepath.Join(client.ConfigDir(), "config.toml")
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	argv := strings.Fields(editor)
	x := &funcExec{run: func(in, out *os.File) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("make %s: %w", filepath.Dir(path), err)
		}
		// The user's own editor, from their $VISUAL or $EDITOR; no timeout,
		// since they edit for as long as they like.
		cmd := exec.CommandContext(context.Background(), argv[0], append(argv[1:], path)...) //nolint:gosec // G702: see above
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, os.Stderr
		return cmd.Run()
	}}
	return m.exec(x, func(err error) tea.Msg {
		if err != nil {
			err = &client.UserError{Msg: "the editor failed: " + err.Error(), Hint: "Set $EDITOR, or edit " + path + " by hand."}
		}
		return editorDoneMsg{err: err}
	})
}
