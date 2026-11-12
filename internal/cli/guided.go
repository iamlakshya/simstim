package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// The guided setup (decision 0020): where agents run, the host, the agent
// checklist, the installs, the logins, and the first session. The engine in
// internal/onboard makes the decisions; this file asks the questions on the
// terminal. The launcher drives the same engine.

type guideOpts struct {
	host, create, name, agents string
	agentsSet, yes, noLogin    bool
}

func setupCmd() *cobra.Command {
	var o guideOpts
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up step by step: a host, its agents, and their logins",
		Long: "A guided setup. It asks where agents will run: a host in your SSH config, a local VM with Lima,\n" +
			"a Docker container, an E2B sandbox, or a server you will rent, which it walks you through.\n" +
			"It connects or creates the host, installs the agents you tick with their official installers,\n" +
			"and offers each agent's own login, each in a session you watch. Ctrl+C stops it at any step,\n" +
			"and it says what was done.\n\n" +
			"Without a terminal, name the host with --host or --create, and pass --yes.",
		Example: "  " + brand.Name + " setup\n" +
			"  " + brand.Name + " setup --host my-server --agents claude,codex\n" +
			"  " + brand.Name + " setup --create lima --agents claude --yes --no-login",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.agentsSet = cmd.Flags().Changed("agents")
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return newGuide(ctx, o, onboard.System{Providers: providers}).run()
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.host, "host", "", "use this host: a saved host, a Host from your SSH config, or user@host")
	f.StringVar(&o.create, "create", "", "make a new host with a provider: lima, docker or e2b")
	f.StringVar(&o.name, "name", "", "the host's name (default: from --host, or the provider's name)")
	f.StringVar(&o.agents, "agents", "", "the agents to install, such as claude,codex (none installs nothing)")
	f.BoolVarP(&o.yes, "yes", "y", false, "do not ask: install the helper, and only the agents in --agents")
	f.BoolVar(&o.noLogin, "no-login", false, "do not offer the agents' logins")
	return cmd
}

// guide asks the setup's questions on a terminal and does what the answers
// say, through the engine.
type guide struct {
	ctx   context.Context
	in    io.Reader
	out   io.Writer
	tty   bool // a terminal on stdin and stdout: prompts and attach work
	local onboard.Local
	o     guideOpts
	log   *onboard.Log
	part  int

	// session runs an install or a login in a session on a host, and
	// returns the session's ID and whether it ended; checks reads a host's
	// agent checks. Tests replace both.
	session func(h client.Host, a onboard.Action) (string, bool, error)
	checks  func(h client.Host) ([]api.AgentCheck, error)
}

func newGuide(ctx context.Context, o guideOpts, local onboard.Local) *guide {
	g := &guide{ctx: ctx, in: os.Stdin, out: os.Stdout, tty: isTerminal(os.Stdin) && isTerminal(os.Stdout),
		local: local, o: o, log: &onboard.Log{}}
	g.session, g.checks = g.runSession, g.agentChecks
	return g
}

// run does the whole setup. After Ctrl+C it says what was done.
func (g *guide) run() error {
	err := g.all()
	if err != nil && (g.ctx.Err() != nil || errors.Is(err, context.Canceled)) {
		return g.stopped(err)
	}
	return err
}

func (g *guide) all() error {
	if err := g.check(); err != nil {
		return err
	}
	fmt.Fprintf(g.out, "Set up %s: a host, its agents, and their logins. Ctrl+C stops at any step, and says what was done.\n", brand.DisplayName)
	h, made, err := g.where()
	if err != nil {
		return err
	}
	g.heading("Connect " + h.Name)
	consent := func(q string) (bool, error) {
		switch {
		case made:
			fmt.Fprintln(g.out, "Making the host counts as your yes.")
			return true, nil
		case g.o.yes:
			return true, nil
		}
		ok, err := g.yesNo(q, false)
		if err == nil && g.ctx.Err() != nil {
			// A Ctrl+C that came with the yes: install nothing.
			return false, g.ctx.Err()
		}
		return ok, err
	}
	checks, err := connectHost(g.ctx, g.out, h, consent, g.log)
	if err != nil {
		return err
	}
	if made && onboard.PowersOffWhenIdle(h.Machine.Provider) {
		if out, err := g.local.SetIdle(g.ctx, h); err != nil {
			fmt.Fprintf(g.out, "  ✕ idle stop: %v\n", err)
		} else {
			fmt.Fprintf(g.out, "  ✓ idle stop: %s\n", client.CleanLine(out))
		}
	}
	f := onboard.NewFlow(h.Name, checks, g.log)
	f.NoLogin = g.o.noLogin || !g.tty
	if err := g.agents(h, f); err != nil {
		return err
	}
	g.done(h, f)
	return nil
}

// check refuses flags that cannot work, before anything changes.
func (g *guide) check() error {
	if g.o.host != "" && g.o.create != "" {
		return &client.UserError{Msg: "--host and --create cannot go together", Hint: "Pass one of them.", Code: "flags_conflict", Kind: client.KindUsage}
	}
	if g.o.create != "" {
		if _, err := findProvider(g.o.create); err != nil {
			return err
		}
	}
	if g.o.agentsSet {
		if err := onboard.CheckAgents(g.o.agents); err != nil {
			return fromFlag(err, "agents")
		}
	}
	if !g.tty && (g.o.host == "" && g.o.create == "" || !g.o.yes) {
		return &client.UserError{
			Msg:  "setup asks its questions on a terminal, and there is none here; flags must answer them",
			Hint: brand.Name + " setup --host <host> --agents claude --yes (or --create lima instead of --host)",
			Code: "answers_needed",
			Kind: client.KindNeedsAnswer,
		}
	}
	return nil
}

// stopped says what was done before Ctrl+C.
func (g *guide) stopped(err error) error {
	fmt.Fprintln(g.out)
	msg := "setup stopped; nothing was changed"
	if lines := g.log.Lines(); len(lines) > 0 {
		fmt.Fprintln(g.out, "Done before the stop:")
		for _, l := range lines {
			fmt.Fprintf(g.out, "  ✓ %s\n", l)
		}
		msg = "setup stopped; nothing else was changed"
	}
	var ue *client.UserError
	if errors.As(err, &ue) && strings.HasPrefix(ue.Msg, "stopped") {
		msg = "setup " + ue.Msg
	}
	return &client.UserError{Msg: msg, Hint: brand.Name + " setup (it picks up what is already done)", Code: "cancelled", Kind: client.KindCancelled}
}

// where asks where agents will run, and returns the host to connect. made
// is true when the setup made the machine, which counts as the user's yes
// to the helper.
func (g *guide) where() (h client.Host, made bool, err error) {
	switch {
	case g.o.host != "":
		h, err = g.namedHost(g.o.host)
		return h, false, err
	case g.o.create != "":
		h, err = g.create(g.o.create)
		return h, true, err
	}
	g.heading("Where will agents run?")
	for {
		places := g.local.Places()
		for i, p := range places {
			mark := "✓"
			if !p.Ready {
				mark = "✕"
			}
			fmt.Fprintf(g.out, "  %d  %-36s %s %s\n", i+1, p.Title, mark, p.Note)
		}
		fmt.Fprintln(g.out)
		i, _, err := g.pick(fmt.Sprintf("Pick one [1-%d]: ", len(places)), len(places), false)
		if err != nil {
			return client.Host{}, false, err
		}
		p, ok, err := g.ready(places[i-1])
		if err != nil {
			return client.Host{}, false, err
		}
		if !ok {
			fmt.Fprintln(g.out)
			continue
		}
		switch p.Kind {
		case onboard.SSH:
			h, err = g.sshHost(p)
		case onboard.Rent:
			h, err = g.rent()
		default:
			h, err = g.create(p.Kind)
			made = true
		}
		return h, made, err
	}
}

// ready shows how to make a place ready, and checks again when the user
// presses Enter. It returns false when the user picks another place.
func (g *guide) ready(p onboard.Place) (onboard.Place, bool, error) {
	for !p.Ready {
		fmt.Fprintf(g.out, "\n%s is not ready on this computer: %s. To make it ready:\n\n", p.Title, p.Note)
		g.steps(p.Steps)
		a, err := g.ask("\nPress Enter to check again, or b to pick another place: ")
		if err != nil {
			return p, false, err
		}
		if strings.EqualFold(a, "b") {
			return p, false, nil
		}
		p, _ = onboard.FindPlace(g.local.Places(), p.Kind)
	}
	return p, true, nil
}

// namedHost is the host --host names: a saved host, or a new one reached by
// an SSH target.
func (g *guide) namedHost(target string) (client.Host, error) {
	if h, err := client.FindHost(target); err == nil {
		return h, nil
	}
	name := g.o.name
	if name == "" {
		name = onboard.NameFor(target)
	}
	return g.newHost(name, target)
}

// newHost checks a new host's name against the saved ones. A saved host
// with the same name and target is used as it is.
func (g *guide) newHost(name, target string) (client.Host, error) {
	if !client.ValidHostName(name) {
		return client.Host{}, &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", name), Hint: "Use letters, digits, dots, dashes and underscores, with --name.", Code: "bad_name", Kind: client.KindUsage}
	}
	if h, err := client.FindHost(name); err == nil {
		if h.SSH == target {
			return h, nil
		}
		return client.Host{}, &client.UserError{Msg: fmt.Sprintf("there is already a host called %s, reached by %s", name, h.Target()), Hint: brand.Name + " setup --host " + shellWord(target) + " --name <another-name>, or " + brand.Name + " setup --host " + shellWord(name), Code: "host_exists"}
	}
	return client.Host{Name: name, SSH: target}, nil
}

// sshHost asks which host in the SSH config, or for a user@host target.
func (g *guide) sshHost(p onboard.Place) (client.Host, error) {
	fmt.Fprintln(g.out)
	var target string
	if len(p.Hosts) > 0 {
		for i, h := range p.Hosts {
			fmt.Fprintf(g.out, "  %d  %s\n", i+1, h)
		}
		fmt.Fprintln(g.out)
		i, text, err := g.pick("Pick a number, or type user@host: ", len(p.Hosts), true)
		if err != nil {
			return client.Host{}, err
		}
		target = text
		if i > 0 {
			target = p.Hosts[i-1]
		}
	}
	for target == "" {
		t, err := g.ask("Type the SSH target, such as dev@203.0.113.10: ")
		if err != nil {
			return client.Host{}, err
		}
		target = t
	}
	name := g.o.name
	if name == "" {
		name = onboard.NameFor(target)
		if name != target {
			a, err := g.ask(fmt.Sprintf("A name for it [%s]: ", name))
			if err != nil {
				return client.Host{}, err
			}
			if a != "" {
				name = a
			}
		}
	}
	for {
		h, err := g.newHost(name, target)
		if err == nil || !g.tty {
			return h, err
		}
		msg, _ := errText(err)
		fmt.Fprintf(g.out, "%s.\n", sentenceCase(msg))
		if name, err = g.ask("Another name: "); err != nil {
			return client.Host{}, err
		}
	}
}

// create makes a host with a provider.
func (g *guide) create(kind string) (client.Host, error) {
	name := g.o.name
	if name == "" {
		name = kind
		if g.tty && g.o.create == "" {
			a, err := g.ask(fmt.Sprintf("\nA name for the new host [%s]: ", kind))
			if err != nil {
				return client.Host{}, err
			}
			if a != "" {
				name = a
			}
		}
	}
	if err := g.ctx.Err(); err != nil {
		// A Ctrl+C that came with the last answer: make nothing.
		return client.Host{}, err
	}
	fmt.Fprintf(g.out, "\nCreating %s with %s…\n", name, kind)
	h, err := g.local.Create(g.ctx, kind, name, g.out)
	if err != nil {
		return client.Host{}, err
	}
	fmt.Fprintf(g.out, "  ✓ created the %s machine %s\n", h.Machine.Provider, h.Machine.ID)
	g.log.Add("made the %s machine %s, and saved the host %s (%s host destroy %s deletes it)", h.Machine.Provider, h.Machine.ID, h.Name, brand.Name, h.Name)
	return h, nil
}

// errText is an error's message, and its hint for a user error.
func errText(err error) (string, string) {
	var ue *client.UserError
	if errors.As(err, &ue) {
		return ue.Msg, ue.Hint
	}
	return err.Error(), ""
}
