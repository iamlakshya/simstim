package tui

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// The picker (docs/contributing/cli-spec.md 6.3): a short list of sessions,
// drawn inline below the command line, for a command whose session is
// missing. It starts from the session cache, so it draws at once, and takes
// the hosts' own lists when they come. Typing filters the rows, enter
// picks, and esc cancels. When it closes it erases its rows, and the
// command goes on as if the user had typed the address.

// pickRows is how many sessions the picker shows at most.
const pickRows = 10

// ErrPickCancelled is the error of a picker closed with esc, ctrl+c or a
// signal.
var ErrPickCancelled = errors.New("the picker was cancelled")

// ErrNoSessions is the error of a picker whose hosts answered with no
// session, and that had none from the cache of a host that did not answer.
var ErrNoSessions = errors.New("no sessions on any host")

// PickRow is one session that the picker offers.
type PickRow struct {
	Host    string
	Session client.Session
}

// Address is the row's address as the user sees it: host/session.
func (r PickRow) Address() string {
	name := r.Session.Name
	if name == "" {
		name = r.Session.ID
	}
	return client.CleanLine(r.Host + "/" + name)
}

// CachedSessions is the session cache's rows for hosts: the sessions each
// host had when the launcher or a command last saw it.
func CachedSessions(hosts []string) []PickRow {
	mem := loadMemory()
	var rows []PickRow
	for _, h := range hosts {
		for _, s := range client.ValidSessions(h, mem.Sessions[h]) {
			rows = append(rows, PickRow{Host: h, Session: s})
		}
	}
	return rows
}

// picker is the picker's model.
type picker struct {
	title   string
	hosts   []string // the hosts that it asks, for the line while it looks
	rows    []PickRow
	filter  []rune
	cur     int // the selected row, in the filtered list
	top     int // the first filtered row on screen
	width   int
	loading bool
	now     func() time.Time
	chosen  *PickRow
	// failed is why the picker closed without a pick: ErrNoSessions, or the
	// error of hosts that did not answer.
	failed error
	done   bool
	// handled, if set, gets whether the picker is done after each key.
	handled chan bool
}

// pickedRows is the hosts' answer to the picker's refresh: the rows, and
// the error when no host answered.
type pickedRows struct {
	rows []PickRow
	err  error
}

// took tells the reader of keys, if any, that the picker took its key or
// paste, and whether it is done.
func (p *picker) took() {
	if p.handled != nil {
		select {
		case p.handled <- p.done:
		default:
		}
	}
}

// pickCancelled closes the picker as esc does: a signal, the end of the
// context, or a terminal that cannot be read. The picker is done before it
// quits, so its last frame erases its rows.
type pickCancelled struct{}

func newPicker(title string, rows []PickRow, now func() time.Time) *picker {
	p := &picker{title: title, width: 80, now: now}
	p.setRows(rows)
	return p
}

// setRows puts rows in order: by host, then as the launcher orders a host's
// sessions.
func (p *picker) setRows(rows []PickRow) {
	byHost := map[string][]client.Session{}
	var hosts []string
	for _, r := range rows {
		if _, ok := byHost[r.Host]; !ok {
			hosts = append(hosts, r.Host)
		}
		byHost[r.Host] = append(byHost[r.Host], r.Session)
	}
	sort.Strings(hosts)
	var keep string
	if sel, ok := p.selected(); ok {
		keep = sel.Host + "/" + sel.Session.ID
	}
	p.rows = p.rows[:0]
	for _, h := range hosts {
		list := client.ValidSessions(h, byHost[h])
		sortSessions(list)
		for _, s := range list {
			p.rows = append(p.rows, PickRow{Host: h, Session: s})
		}
	}
	p.cur, p.top = 0, 0
	for i, r := range p.matches() {
		if r.Host+"/"+r.Session.ID == keep {
			p.cur = i
		}
	}
	p.scroll()
}

// matches is the rows whose address holds the filter, in any case.
func (p *picker) matches() []PickRow {
	q := strings.ToLower(string(p.filter))
	var out []PickRow
	for _, r := range p.rows {
		if strings.Contains(strings.ToLower(r.Address()), q) {
			out = append(out, r)
		}
	}
	return out
}

func (p *picker) selected() (PickRow, bool) {
	m := p.matches()
	if p.cur < 0 || p.cur >= len(m) {
		return PickRow{}, false
	}
	return m[p.cur], true
}

// scroll keeps the selected row on screen.
func (p *picker) scroll() {
	n := len(p.matches())
	p.cur = max(0, min(p.cur, n-1))
	if p.cur < p.top {
		p.top = p.cur
	}
	if p.cur >= p.top+pickRows {
		p.top = p.cur - pickRows + 1
	}
	p.top = max(0, min(p.top, n-pickRows))
}

// Init does nothing: Pick asks the hosts, and sends their answer.
func (p *picker) Init() tea.Cmd { return nil }

// Update takes keys, the terminal's size and the hosts' lists. Lists with no
// session close the picker: with ErrNoSessions when a host answered, and
// with the hosts' error when none did.
func (p *picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
	case pickCancelled:
		p.done = true
		return p, tea.Quit
	case pickedRows:
		p.loading = false
		p.setRows(msg.rows)
		if len(p.rows) == 0 {
			p.failed, p.done = msg.err, true
			if p.failed == nil {
				p.failed = ErrNoSessions
			}
			return p, tea.Quit
		}
	case tea.KeyPressMsg:
		cmd := p.key(msg)
		p.took()
		return p, cmd
	case tea.PasteMsg:
		// A paste goes into the filter, without its line breaks: only an
		// Enter of its own picks a row.
		if text := keyread.OneLine(msg.Content); text != "" {
			p.filter = append(p.filter, []rune(text)...)
			p.cur, p.top = 0, 0
			p.scroll()
		}
		p.took()
		return p, nil
	}
	return p, nil
}

func (p *picker) key(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c":
		p.done = true
		return tea.Quit
	case "enter":
		if r, ok := p.selected(); ok {
			p.chosen, p.done = &r, true
			return tea.Quit
		}
	case "up", "ctrl+p":
		p.cur--
	case "down", "ctrl+n":
		p.cur++
	case "backspace":
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
			p.cur, p.top = 0, 0
		}
	case "ctrl+u":
		p.filter, p.cur, p.top = nil, 0, 0
	default:
		if t := k.Text; t != "" && !strings.ContainsAny(t, "\r\n\t") {
			p.filter = append(p.filter, []rune(t)...)
			p.cur, p.top = 0, 0
		}
	}
	p.scroll()
	return nil
}

// View draws the picker inline. Once it is done it draws nothing, so its
// rows are erased.
func (p *picker) View() tea.View {
	if p.done {
		return tea.NewView("")
	}
	return tea.NewView(p.render())
}

// render is the picker's text: the filter line, then at most pickRows
// sessions.
func (p *picker) render() string {
	m := p.matches()
	w := max(p.width, 20)
	var b strings.Builder
	left := sTitle.Render(p.title) + " " + sAccent.Render(glyphPrompt) + " " + string(p.filter) + sKey.Reverse(true).Render(" ")
	hint := fmt.Sprintf("%d of %d", len(m), len(p.rows))
	if p.loading {
		hint += dot + "checking hosts…"
	}
	hint += dot + "↑↓ enter esc"
	gap := w - cells(left) - cells(hint)
	if gap < 2 {
		b.WriteString(left)
	} else {
		b.WriteString(left + strings.Repeat(" ", gap) + sFaint.Render(hint))
	}
	switch {
	case len(p.rows) == 0 && p.loading:
		b.WriteString("\n" + sFaint.Render("  Looking for sessions on "+listNames(p.hosts)+"…"))
		return b.String()
	case len(m) == 0:
		b.WriteString("\n" + sFaint.Render("  No session matches."))
		return b.String()
	}
	addrW, agentW := 0, 0
	for _, r := range m {
		addrW = max(addrW, cells(r.Address()))
		agentW = max(agentW, cells(client.CleanLine(r.Session.Agent)))
	}
	addrW = min(addrW, max(12, w/2))
	now := p.now()
	for i := p.top; i < len(m) && i < p.top+pickRows; i++ {
		r := m[i]
		d := shown(r.Session)
		state := strings.ReplaceAll(d.State, "_", " ")
		line := fit(r.Address(), addrW) + "  " + fit(d.Agent, agentW) + "  " + fit(state, 12) + " " + fitRight(fmtAge(now.Sub(d.CreatedAt)), 4)
		line = trunc(line, w-2)
		if i == p.cur {
			b.WriteString("\n" + sAccent.Render(glyphMarker) + " " + sBold.Render(line))
		} else {
			b.WriteString("\n  " + line)
		}
	}
	return b.String()
}

// listNames names items for a sentence: "box", "box and nuc", "box, nuc and lab".
func listNames(items []string) string {
	switch len(items) {
	case 0:
		return "your hosts"
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
