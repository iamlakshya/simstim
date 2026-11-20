package onboard

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Item is one row of the agent checklist.
type Item struct {
	Agent agents.Agent
	// Check is what the host said about the agent. Its Kind is empty when the
	// host said nothing: the host checks every first-class and supported
	// agent, and any other that is installed.
	Check api.AgentCheck
	// Chosen is ticked: install it. An installed agent is always ticked.
	Chosen bool
}

// Installed reports whether the host has the agent.
func (i Item) Installed() bool { return i.Check.Installed }

// BestEffort reports whether the agent is only kept working by contributors.
// The checklists fold these away.
func (i Item) BestEffort() bool { return i.Agent.Support == agents.BestEffort }

// State is the item's state in a few words.
func (i Item) State() string { return Describe("", i.Check).Short }

// Checklist lists every agent that has an installer: first-class and
// supported agents first, then best-effort ones, each in the agent table's
// order. Installed agents are ticked.
func Checklist(checks []api.AgentCheck) []Item {
	var top, rest []Item
	for _, a := range agents.Installable() {
		it := Item{Agent: a}
		for _, c := range checks {
			if c.Kind == a.Kind {
				it.Check = c
			}
		}
		it.Chosen = it.Installed()
		if it.BestEffort() {
			rest = append(rest, it)
		} else {
			top = append(top, it)
		}
	}
	return append(top, rest...)
}

// What a Flow asks its driver to do next.
const (
	ActInstall = "install"
	ActLogin   = "login"
	ActDone    = "done"
)

// Action is the next thing to do in a Flow.
type Action struct {
	Kind  string
	Agent agents.Agent
	// Args start the session that does it, for an install or a login.
	Args client.NewArgs
	// Session is the ID of that session, once the driver has started it.
	Session string
	// N counts this action among those of its kind, of Of: "2 of 3".
	N, Of int
}

// Flow is the agent part of a guided setup on one host: the checklist, then
// each chosen install, then each login, in order. A driver shows the
// checklist (Items, Toggle, or Choose for a flag), calls Start, then asks Next
// what to do. An install or a login runs in a session the user watches; when
// the user leaves it, the driver gives the host's new checks to Done. The
// setup command and the launcher drive the same Flow.
type Flow struct {
	Host  string
	Items []Item
	// NoLogin leaves out the logins.
	NoLogin bool
	// Log gets a line for each thing done.
	Log *Log

	started  bool
	installs []string // kinds still to install
	logins   []string // kinds still offered a login
	loginsAt int      // how many logins were offered in all
	installN int      // how many installs there were in all
	notes    map[string]string
}

// NewFlow starts a flow for host from the host's agent checks.
func NewFlow(host string, checks []api.AgentCheck, log *Log) *Flow {
	if log == nil {
		log = &Log{}
	}
	return &Flow{Host: host, Items: Checklist(checks), Log: log, notes: map[string]string{}}
}

// Missing reports whether a first-class or supported agent is not
// installed, so the checklist is worth showing.
func (f *Flow) Missing() bool {
	for _, it := range f.Items {
		if !it.BestEffort() && !it.Installed() {
			return true
		}
	}
	return false
}

// Toggle ticks or unticks item i. An installed agent stays ticked.
func (f *Flow) Toggle(i int) {
	if i >= 0 && i < len(f.Items) && !f.Items[i].Installed() {
		f.Items[i].Chosen = !f.Items[i].Chosen
	}
}

// Choose ticks the agents in a comma-separated list, the value of --agents.
// "shell", which is always there, and "none" install nothing.
func (f *Flow) Choose(list string) error {
	for _, k := range strings.Split(list, ",") {
		k = strings.TrimSpace(k)
		if k == "" || k == "none" || k == "shell" {
			continue
		}
		i := slices.IndexFunc(f.Items, func(it Item) bool { return it.Agent.Kind == k })
		if i < 0 {
			return &client.UserError{Msg: fmt.Sprintf("there is no agent called %q to install", k), Hint: brand.Name + " agents ls   (the first column names each agent)"}
		}
		f.Items[i].Chosen = true
	}
	return nil
}

// CheckAgents checks a list for Choose before anything is done.
func CheckAgents(list string) error { return NewFlow("", nil, nil).Choose(list) }

// Chosen lists the agents ticked to install that the host does not have.
func (f *Flow) Chosen() []agents.Agent {
	var out []agents.Agent
	for _, it := range f.Items {
		if it.Chosen && !it.Installed() {
			out = append(out, it.Agent)
		}
	}
	return out
}

// Start ends the checklist: the installs come next.
func (f *Flow) Start() {
	if f.started {
		return
	}
	f.started = true
	for _, a := range f.Chosen() {
		f.installs = append(f.installs, a.Kind)
	}
	f.installN = len(f.installs)
}

// Next says what to do now: install an agent, offer a login, or finish.
func (f *Flow) Next() Action {
	f.Start()
	if len(f.installs) > 0 {
		a, _ := agents.Find(f.installs[0])
		return Action{Kind: ActInstall, Agent: a, Args: InstallArgs(a), N: f.installN - len(f.installs) + 1, Of: f.installN}
	}
	if f.logins == nil && !f.NoLogin {
		f.logins = []string{}
		for _, it := range f.Items {
			if NeedsLogin(it) {
				f.logins = append(f.logins, it.Agent.Kind)
			}
		}
		f.loginsAt = len(f.logins)
	}
	if len(f.logins) > 0 {
		a, _ := agents.Find(f.logins[0])
		args, _ := LoginArgs(a)
		return Action{Kind: ActLogin, Agent: a, Args: args, N: f.loginsAt - len(f.logins) + 1, Of: f.loginsAt}
	}
	return Action{Kind: ActDone}
}

// NeedsLogin reports whether an item is installed and ready to start, but
// not logged in, and has a login command to run.
func NeedsLogin(it Item) bool {
	c := it.Check
	return c.Installed && c.OnPath && c.Login == api.LoginNo && it.Agent.Setup != nil && it.Agent.Setup.Login != ""
}

// Done records how an install or a login went, from the host's checks after
// the user left its session. ended says whether the session ended, rather
// than the user detaching from it.
func (f *Flow) Done(a Action, checks []api.AgentCheck, ended bool) {
	f.update(checks)
	it := f.item(a.Agent.Kind)
	// A command names the session by its ID, or quoted (client.CommandTarget).
	session := client.CommandTarget(f.Host, a.Args.Name)
	if a.Session != "" {
		session = client.CommandTarget(f.Host, a.Session)
	}
	switch a.Kind {
	case ActInstall:
		f.installs = f.installs[1:]
		switch {
		case it.Installed():
			f.Log.Add("installed %s on %s", a.Agent.Title, f.Host)
		case !ended:
			f.notes[a.Agent.Kind] = "its installer still runs: " + brand.Name + " attach " + session
		default:
			f.notes[a.Agent.Kind] = "its installer ended, but it is not installed: read its output with " + brand.Name + " attach " + session
		}
	case ActLogin:
		f.logins = f.logins[1:]
		switch {
		case it.Check.Login == api.LoginYes:
			f.Log.Add("logged in to %s on %s", a.Agent.Title, f.Host)
		case !ended:
			f.notes[a.Agent.Kind] = "its login still runs: " + brand.Name + " attach " + session
		}
	}
}

// Skip passes over an action: the user chose not to do it now.
func (f *Flow) Skip(a Action) {
	switch a.Kind {
	case ActInstall:
		if len(f.installs) > 0 {
			f.installs = f.installs[1:]
		}
	case ActLogin:
		if len(f.logins) > 0 {
			f.logins = f.logins[1:]
		}
	}
}

// Stop drops every action left, as when the user stops the setup.
func (f *Flow) Stop() {
	f.Start()
	f.installs, f.logins = nil, []string{}
}

func (f *Flow) update(checks []api.AgentCheck) {
	for i := range f.Items {
		for _, c := range checks {
			if c.Kind == f.Items[i].Agent.Kind {
				f.Items[i].Check = c
				if c.Installed {
					f.Items[i].Chosen = true
				}
			}
		}
	}
}

func (f *Flow) item(kind string) Item {
	for _, it := range f.Items {
		if it.Agent.Kind == kind {
			return it
		}
	}
	return Item{}
}

// Shown are the agents a summary covers: every first-class and supported
// agent, and any other that is installed or was chosen.
func (f *Flow) Shown() []Item {
	var out []Item
	for _, it := range f.Items {
		if !it.BestEffort() || it.Installed() || it.Chosen {
			out = append(out, it)
		}
	}
	return out
}

// Summary describes each agent of Shown, and what to try next.
func (f *Flow) Summary() []Line {
	shown := f.Shown()
	out := make([]Line, 0, len(shown))
	for _, it := range shown {
		out = append(out, f.Line(it.Agent.Kind))
	}
	return out
}

// Line describes one agent, with the note of its last install or login.
func (f *Flow) Line(kind string) Line {
	it := f.item(kind)
	c := it.Check
	if c.Kind == "" {
		c = api.AgentCheck{Kind: it.Agent.Kind, Title: it.Agent.Title}
	}
	l := Describe(f.Host, c)
	if n := f.notes[kind]; n != "" {
		l.Try = n
	}
	return l
}

// FirstAgent is the agent for the first session: the first in the table's
// order that is ready and logged in, else one whose login is unknown, else
// any that is ready. It is "" when none is, for the shell.
func (f *Flow) FirstAgent() string {
	for _, want := range []string{api.LoginYes, api.LoginUnknown, api.LoginNo} {
		for _, it := range f.Items {
			c := it.Check
			if c.Installed && c.OnPath && c.Login == want {
				return it.Agent.Kind
			}
		}
	}
	return ""
}

// FirstSession is the command that starts the first session on the host,
// with FirstAgent.
func (f *Flow) FirstSession() string {
	if a := f.FirstAgent(); a != "" {
		return fmt.Sprintf("%s new %s --agent %s", brand.Name, f.Host, a)
	}
	return fmt.Sprintf("%s new %s", brand.Name, f.Host)
}

// InstallArgs start an agent's official installer in a session named
// install-<agent>, so the user sees its output and can answer it.
func InstallArgs(a agents.Agent) client.NewArgs {
	return client.NewArgs{Name: "install-" + a.Kind, Cwd: "~", Argv: []string{"/bin/sh", "-c", a.Setup.Install}, Setup: api.SetupSkip}
}

// LoginArgs start an agent's own login in a session named login-<agent>. An
// agent whose first start is its login starts as itself. The product never
// sees or handles the login.
func LoginArgs(a agents.Agent) (client.NewArgs, bool) {
	if a.Setup == nil || a.Setup.Login == "" {
		return client.NewArgs{}, false
	}
	args := client.NewArgs{Name: "login-" + a.Kind, Cwd: "~", Setup: api.SetupSkip}
	if a.Setup.Login == a.Binaries[0] {
		args.Agent = a.Kind
	} else {
		args.Argv = []string{"/bin/sh", "-c", a.Setup.Login}
	}
	return args, true
}

// LoginCommand is the command that starts an agent's own login in a new
// session on host. An agent whose login is its first start starts as itself.
func LoginCommand(host string, a agents.Agent) string {
	if a.Setup == nil || a.Setup.Login == "" {
		return ""
	}
	if a.Setup.Login == a.Binaries[0] {
		return fmt.Sprintf("%s new %s --agent %s", brand.Name, host, a.Kind)
	}
	return fmt.Sprintf("%s new %s -- %s", brand.Name, host, a.Setup.Login)
}

// Log is what a guided setup changed, in order, so that a stop at any step
// can say what was done.
type Log struct{ lines []string }

// Add records one thing done.
// A nil Log records nothing.
func (l *Log) Add(format string, a ...any) {
	if l != nil {
		l.lines = append(l.lines, fmt.Sprintf(format, a...))
	}
}

// Lines are the things done, in order.
func (l *Log) Lines() []string { return append([]string(nil), l.lines...) }
