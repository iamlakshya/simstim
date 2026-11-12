package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// The guided setup (docs/contributing/ux-spec.md 2.12): where agents will
// run, then a host to make or a server to rent, step by step. It hands the
// host to the add-host wizard, which connects it, then shows the agent
// checklist, the installs and the logins. internal/onboard decides; this
// sheet only asks.

type gdStep int

const (
	gdWhere      gdStep = iota
	gdName              // a name for the host to make
	gdCreate            // the provider makes it
	gdRentKey           // the rent walk-through, one step per screen
	gdRentServer        // the server's address
	gdRentUser          // the user for agents, and a check that ssh works
	gdRentName          // the host's name
	gdRentConfig        // the lines for ~/.ssh/config, and a check
)

type guide struct {
	step    gdStep
	places  []onboard.Place
	sel     int
	kind    string // the place chosen
	in      input
	plan    onboard.RentPlan
	present bool // ~/.ssh/config already names the host
	asked   bool // the user said they add the lines themselves
	checks  []check
	err     error
	note    string

	run     bool
	seq     int
	cancel  context.CancelFunc
	done    chan struct{} // closed when a create has finished, clean-up included
	out     *lastLine     // the provider's progress
	pending string        // a place to choose once the probe answers
}

// Guided setup results.
type (
	placesMsg struct {
		places []onboard.Place
		choose string
	}
	createdMsg struct {
		seq  int
		host client.Host
		err  error
		took time.Duration
	}
	rentKeyMsg   struct{ key string }
	rentCheckMsg struct {
		seq  int
		err  error
		took time.Duration
	}
	rentNameMsg struct{ present bool }
)

// openGuide opens the guided setup on "where will agents run?".
func (m *Model) openGuide() tea.Cmd {
	g := &guide{}
	m.modal = g
	return g.probe(m, "")
}

func (g *guide) busy() bool { return g.run }

func (g *guide) paste(s string) {
	if g.typing() {
		g.in.insert(s)
	}
}

// typing reports whether the step has a text field.
func (g *guide) typing() bool {
	switch g.step {
	case gdName, gdRentServer, gdRentUser, gdRentName:
		return true
	default:
		// The other steps have no text field.
	}
	return false
}

// probe reads where agents can run, then chooses choose if it is ready.
func (g *guide) probe(m *Model, choose string) tea.Cmd {
	local := m.be.Local()
	return func() tea.Msg { return placesMsg{places: local.Places(), choose: choose} }
}

func (g *guide) begin(m *Model) (context.Context, int) {
	g.halt()
	g.run, g.err = true, nil
	ctx, cancel := context.WithCancel(m.ctx)
	g.cancel = cancel
	return ctx, g.seq
}

// halt stops a running step, and waits for a create to clean up after
// itself, so quitting leaves no half-made machine.
func (g *guide) halt() {
	g.seq++
	g.run = false
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	if g.done != nil {
		<-g.done
		g.done = nil
	}
}

func (g *guide) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	if ks == "esc" {
		return g.back(m)
	}
	if g.run {
		return nil
	}
	switch g.step {
	case gdWhere:
		return g.whereKey(m, ks)
	case gdName:
		if ks == "enter" {
			return g.create(m)
		}
	case gdCreate:
		if ks == "enter" && g.err != nil {
			g.step, g.err, g.checks = gdName, nil, nil
		}
		return nil
	case gdRentKey:
		return g.keyKey(m, ks)
	case gdRentServer:
		if ks == "enter" {
			addr := strings.TrimSpace(g.in.value())
			if !onboard.ValidAddr(addr) {
				g.err = &client.UserError{Msg: "type the server's address", Hint: "Type its IP address or DNS name, as the provider's console shows it.", Code: "missing_value", Kind: client.KindUsage}
				return nil
			}
			g.plan.Addr, g.err = addr, nil
			g.step, g.in = gdRentUser, newInput(g.userOr(), onboard.RentUser)
			return nil
		}
	case gdRentUser:
		if ks == "enter" {
			return g.checkUser(m)
		}
	case gdRentName:
		if ks == "enter" {
			return g.nameServer(m)
		}
	case gdRentConfig:
		return g.configKey(m, ks)
	}
	if g.typing() && g.in.key(k) {
		g.err = nil
		if g.step == gdRentUser {
			g.checks = nil
		}
	}
	return nil
}

func (g *guide) userOr() string {
	if g.plan.User != "" {
		return g.plan.User
	}
	return onboard.RentUser
}

// back goes one step back, or closes the sheet from the first step. A
// running create is stopped first, and its clean-up shows.
func (g *guide) back(m *Model) tea.Cmd {
	if g.step == gdCreate && g.run {
		if g.cancel != nil {
			g.cancel()
			g.cancel = nil
		}
		g.note = "Stopping, and deleting what was begun…"
		return nil
	}
	g.halt()
	g.err, g.checks, g.note = nil, nil, ""
	switch g.step {
	case gdWhere:
		m.modal = nil
	case gdName, gdCreate, gdRentKey:
		g.step = gdWhere
	case gdRentServer:
		g.step = gdRentKey
	case gdRentUser:
		g.plan.User = strings.TrimSpace(g.in.value())
		g.step, g.in = gdRentServer, newInput(g.plan.Addr, "203.0.113.10")
	case gdRentName:
		g.step, g.in = gdRentUser, newInput(g.userOr(), onboard.RentUser)
	case gdRentConfig:
		g.step, g.in, g.asked = gdRentName, newInput(g.plan.Name, onboard.RentName), false
	}
	return nil
}

func (g *guide) whereKey(m *Model, ks string) tea.Cmd {
	n := len(g.places)
	switch ks {
	case "up", "k":
		g.sel = max(g.sel-1, 0)
	case "down", "j":
		g.sel = min(g.sel+1, max(n-1, 0))
	case "r":
		return g.probe(m, "")
	case "enter":
		if g.sel < n {
			return g.choose(m, g.places[g.sel])
		}
	default:
		if len(ks) == 1 && ks >= "1" && ks <= "9" && int(ks[0]-'1') < n {
			g.sel = int(ks[0] - '1')
			return g.choose(m, g.places[g.sel])
		}
	}
	g.err = nil
	return nil
}

// choose goes on with a place. A place that is not ready is checked again
// first: the user may have just done its steps.
func (g *guide) choose(m *Model, p onboard.Place) tea.Cmd {
	if !p.Ready {
		if g.pending == p.Kind {
			g.pending = ""
			g.err = &client.UserError{Msg: p.Title + " is not ready yet: " + p.Note, Hint: "Do the steps above, then press enter again.", Code: "not_ready"}
			return nil
		}
		return g.probe(m, p.Kind)
	}
	g.pending, g.err, g.kind = "", nil, p.Kind
	switch p.Kind {
	case onboard.SSH:
		w := newWizard()
		w.guided, w.prev = true, g
		w.known = p.Hosts
		m.modal = w
		return nil
	case onboard.Rent:
		g.step, g.plan = gdRentKey, onboard.RentPlan{User: onboard.RentUser}
		return g.findKey(m)
	}
	g.step, g.in = gdName, newInput(p.Kind, p.Kind)
	return nil
}

func (g *guide) onPlaces(m *Model, msg placesMsg) tea.Cmd {
	g.places = msg.places
	g.sel = min(g.sel, max(len(g.places)-1, 0))
	if msg.choose == "" {
		return nil
	}
	p, ok := onboard.FindPlace(g.places, msg.choose)
	if !ok {
		return nil
	}
	g.pending = msg.choose
	return g.choose(m, p)
}

// create makes the host with the provider, in the background.
func (g *guide) create(m *Model) tea.Cmd {
	name := strings.TrimSpace(g.in.value())
	if name == "" {
		name = g.kind
	}
	switch {
	case !client.ValidHostName(name):
		g.err = &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", name), Hint: "Use letters, digits, dots, dashes and underscores.", Code: "bad_name", Kind: client.KindUsage}
		return nil
	case m.hostByName(name) != nil:
		g.err = &client.UserError{Msg: "there is already a host named " + name, Hint: "Pick another name.", Code: "host_exists"}
		return nil
	}
	g.step, g.note = gdCreate, ""
	g.checks = []check{{name: "create", state: ckRunning, detail: "with " + g.kind}}
	ctx, seq := g.begin(m)
	done := make(chan struct{})
	g.done, g.out = done, &lastLine{}
	local, kind, out, clock := m.be.Local(), g.kind, g.out, m.now
	return func() tea.Msg {
		defer close(done)
		start := clock()
		h, err := local.Create(ctx, kind, name, out)
		return createdMsg{seq: seq, host: h, err: err, took: clock().Sub(start)}
	}
}

func (g *guide) onCreated(m *Model, msg createdMsg) tea.Cmd {
	if g.done != nil {
		g.done = nil // the command has finished
	}
	if msg.seq != g.seq {
		return nil
	}
	g.run, g.cancel, g.note = false, nil, ""
	if msg.err != nil {
		g.checks[0].state = ckFailed
		g.err = msg.err
		return nil
	}
	// The wizard connects it: creating it was the user's yes to the helper.
	w := newWizard()
	w.guided, w.made = true, true
	w.host = msg.host
	m.modal = w
	return w.probe(m)
}

// The rent walk-through.

// onMsg takes the results of the guide's commands.
func (g *guide) onMsg(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case placesMsg:
		return g.onPlaces(m, msg)
	case createdMsg:
		return g.onCreated(m, msg)
	case rentKeyMsg:
		g.plan.Key = msg.key
		return nil
	case rentCheckMsg:
		return g.onRentCheck(m, msg)
	case rentNameMsg:
		g.present, g.asked = msg.present, false
		g.step, g.checks, g.note = gdRentConfig, nil, ""
		return nil
	}
	return nil
}

// lastLine keeps the last line a provider wrote, for the sheet to show.
type lastLine struct {
	mu   sync.Mutex
	buf  string
	last string
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf += string(p)
	for {
		i := strings.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if s := strings.TrimSpace(l.buf[:i]); s != "" {
			l.last = s
		}
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

func (l *lastLine) String() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}
