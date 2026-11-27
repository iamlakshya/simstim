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
	host, create, name, tier, agents string
	agentsSet, yes, noLogin          bool
}

func setupCmd() *cobra.Command {
	var o guideOpts
	var agentList []string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up step by step: a host, its agents, and their logins",
		Long: "A guided setup. It asks where agents will run: a host in your SSH config, a local VM with Lima,\n" +
			"a Docker container, an E2B sandbox, or a server you will rent, which it walks you through.\n" +
			"It connects or creates the host, installs the agents you tick with their official installers,\n" +
			"and offers each agent's own login, each in a session you watch. Ctrl+C stops it at any step,\n" +
			"and it says what was done.\n\n" +
			"Without a terminal, name the host with --host (and --tier) or --create, and pass --yes.",
		Example: "  " + brand.Name + " setup\n" +
			"  " + brand.Name + " setup --host my-vps --agents claude,codex\n" +
			"  " + brand.Name + " setup --create lima --agents claude --yes --no-login",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.agentsSet = cmd.Flags().Changed("agents")
			o.agents = strings.Join(agentList, ",")
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			g := newGuide(ctx, o, onboard.System{Providers: providers})
			g.p = prompts(cmd)
			g.p.ctx, g.p.log = ctx, g.log
			g.tty = g.p.on
			return g.run()
		},
	}
	f := cmd.Flags()
	addHostFlag(cmd, &o.host, "use this host: a saved host, a Host from your SSH config, or user@host")
	f.StringVar(&o.create, "create", "", "make a new host with a provider: lima, docker or e2b")
	addName(cmd, &o.name, "the host's name (default: from --host, or the provider's name)")
	f.StringVar(&o.tier, "tier", "", "who else has root on the host: own, throwaway or shared")
	addList(f, &agentList, "agents", "the agents to install, such as claude,codex (none installs nothing)")
	addYes(cmd, &o.yes, "install the helper, and install no agent outside --agents")
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
	// p builds the error of a question that input off leaves without an
	// answer, from the command line.
	p           *prompter
	log         *onboard.Log
	part        int
	readerStuck bool

	// session runs an install or a login in a session on a host, and
	// returns the session's ID and whether it ended; checks reads a host's
	// agent checks. Tests replace both.
	session func(h client.Host, a onboard.Action) (string, bool, error)
	checks  func(h client.Host) ([]api.AgentCheck, error)
	// unlockHost lets go of the lock of the host that the setup makes,
	// which it holds from the create through the save of the connect.
	unlockHost func()
	baseCtx    context.Context
}

func newGuide(ctx context.Context, o guideOpts, local onboard.Local) *guide {
	tty := isTerminal(os.Stdin) && isTerminal(os.Stdout) && isTerminal(os.Stderr)
	g := &guide{ctx: ctx, in: os.Stdin, out: os.Stderr, tty: tty,
		local: local, o: o, log: &onboard.Log{}, p: &prompter{ctx: ctx, in: os.Stdin, out: os.Stderr, on: tty, path: brand.Name + " setup"}}
	g.session, g.checks = g.runSession, g.agentChecks
	return g
}

// run does the whole setup. After Ctrl+C it says what was done.
func (g *guide) run() error {
	defer g.releaseHost()
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
	h, was, made, err := g.where()
	if err != nil {
		return err
	}
	tier, chooseTier, err := g.resolveTier(h, made)
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
	// Preserve the captured entry and owned pin context through setup.
	checks, err := connectHost(g.ctx, g.out, client.HostChange{Kind: "setup", Was: was, Now: &h, Pin: true, Tier: tier}, chooseTier, consent, g.log, hostKeyOptions(h, "", made || g.o.yes, g.tty))
	g.releaseHost()
	if err != nil {
		if errors.Is(err, client.ErrUnchecked) {
			return g.p.needs(qSetup, "--yes to pin the host key of "+h.Name+" on first use", "--yes")
		}
		return err
	}
	if made && onboard.PowersOffWhenIdle(h.Machine.Provider) {
		if out, err := g.local.SetIdle(g.ctx, h); err != nil {
			fmt.Fprintf(g.out, "  ✕ idle stop: %s\n", shown(err.Error()))
		} else {
			idleSaid(g.out, out)
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
	if g.o.tier != "" {
		if _, err := client.ParseTier(g.o.tier); err != nil {
			return err
		}
	}
	if g.o.agentsSet {
		if err := onboard.CheckAgents(g.o.agents); err != nil {
			return fromFlag(err, "agents")
		}
	}
	if !g.tty && (g.o.host == "" && g.o.create == "" || !g.o.yes) {
		// Input is off: the flags must answer every question (qSetup).
		var flags []string
		if g.o.host == "" && g.o.create == "" {
			flags = append(flags, "--host", "<host>", "--tier", "<tier>")
		} else if g.o.host != "" && g.o.tier == "" {
			flags = append(flags, "--tier", "<tier>")
		}
		if !g.o.yes {
			flags = append(flags, "--yes")
		}
		return g.p.needs(qSetup, "--host or --create, with --yes, to answer its questions", flags...)
	}
	return nil
}

func (g *guide) resolveTier(h client.Host, made bool) (client.Tier, func() (client.Tier, error), error) {
	g.p.in, g.p.out, g.p.on = g.in, g.out, g.tty
	if g.o.tier != "" {
		t, err := client.ParseTier(g.o.tier)
		return t, nil, err
	}
	if made {
		return client.ProviderTier(h.Machine.Provider), nil, nil
	}
	hasTier, err := hasSavedHostTier(g.ctx, h.Name)
	if err != nil || hasTier {
		return client.TierUnknown, nil, err
	}
	if g.o.yes || !g.tty {
		return client.TierUnknown, nil, missingTierWithYes(g.p, qSetup, h.Name)
	}
	return client.TierUnknown, func() (client.Tier, error) { return g.p.askTier(h.Name) }, nil
}

// stopped says what was done before Ctrl+C.
// A stop while a machine is made says what became of the machine. Any other
// stop is a cancel, whose line lists what was done.
func (g *guide) stopped(err error) error {
	fmt.Fprintln(g.out)
	var ue *client.UserError
	if errors.As(err, &ue) && strings.HasPrefix(ue.Msg, "stopped") {
		return &client.UserError{Msg: "setup " + ue.Msg, Hint: brand.Name + " setup (it picks up what is already done)", Code: "cancelled", Kind: client.KindCancelled}
	}
	return cancelled(g.log.Lines()...)
}

// where asks where agents will run, and returns the host to connect, and
// the saved entry that it read for it, or nil for a new host. made is true
// when the setup made the machine, which counts as the user's yes to the
// helper.
func (g *guide) where() (h client.Host, was *client.Host, made bool, err error) {
	switch {
	case g.o.host != "":
		h, was, err = g.namedHost(g.o.host)
		return h, was, false, err
	case g.o.create != "":
		h, err = g.create(g.o.create)
		return h, savedAs(h), true, err
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
			return client.Host{}, nil, false, err
		}
		p, ok, err := g.ready(places[i-1])
		if err != nil {
			return client.Host{}, nil, false, err
		}
		if !ok {
			fmt.Fprintln(g.out)
			continue
		}
		switch p.Kind {
		case onboard.SSH:
			h, was, err = g.sshHost(p)
		case onboard.Rent:
			h, err = g.rent()
		default:
			h, err = g.create(p.Kind)
			was, made = savedAs(h), true
		}
		return h, was, made, err
	}
}

// savedAs is a copy of h, the entry that the create saved, as the Was of
// the save after it.
func savedAs(h client.Host) *client.Host { return &h }

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
// an SSH target. was is the saved entry, or nil for a new host.
func (g *guide) namedHost(target string) (h client.Host, was *client.Host, err error) {
	if was, err = client.SavedEntry(target); err != nil || was != nil {
		return savedOrNone(was), was, err
	}
	name := g.o.name
	if name == "" {
		name = onboard.NameFor(target)
	}
	return g.newHost(name, target)
}

// savedOrNone is the host of a saved entry, or no host.
func savedOrNone(was *client.Host) client.Host {
	if was == nil {
		return client.Host{}
	}
	return *was
}

// newHost checks a new host's name against the saved ones. A saved host
// with the same name and target is used as it is: was is its entry, or nil
// for a new host.
func (g *guide) newHost(name, target string) (client.Host, *client.Host, error) {
	if !client.ValidHostName(name) {
		return client.Host{}, nil, &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", name), Hint: "Use letters, digits, dots, dashes and underscores, with no dot at the end, with --name.", Code: "bad_name", Kind: client.KindUsage}
	}
	saved, err := client.SavedEntry(name)
	switch {
	case err != nil:
		return client.Host{}, nil, err
	case saved != nil && saved.SSH == target:
		return *saved, saved, nil
	case saved != nil:
		h := *saved
		return client.Host{}, nil, &client.UserError{Msg: fmt.Sprintf("there is already a host called %s, reached by %s", name, h.Target()), Hint: brand.Name + " setup --host " + shellWord(target) + " --name <another-name>, or " + brand.Name + " setup --host " + shellWord(name), Code: "host_exists"}
	}
	return client.Host{Name: name, SSH: target}, nil, nil
}

// sshHost asks which host in the SSH config, or for a user@host target. was
// is the saved entry of the host, or nil for a new host.
func (g *guide) sshHost(p onboard.Place) (client.Host, *client.Host, error) {
	fmt.Fprintln(g.out)
	var target string
	if len(p.Hosts) > 0 {
		for i, h := range p.Hosts {
			fmt.Fprintf(g.out, "  %d  %s\n", i+1, h)
		}
		fmt.Fprintln(g.out)
		i, text, err := g.pick("Pick a number, or type user@host: ", len(p.Hosts), true)
		if err != nil {
			return client.Host{}, nil, err
		}
		target = text
		if i > 0 {
			target = p.Hosts[i-1]
		}
	}
	for target == "" {
		t, err := g.ask("Type the SSH target, such as dev@203.0.113.10: ")
		if err != nil {
			return client.Host{}, nil, err
		}
		target = t
	}
	name := g.o.name
	if name == "" {
		name = onboard.NameFor(target)
		if name != target {
			a, err := g.ask(fmt.Sprintf("A name for it [%s]: ", name))
			if err != nil {
				return client.Host{}, nil, err
			}
			if a != "" {
				name = a
			}
		}
	}
	for {
		h, was, err := g.newHost(name, target)
		if err == nil || !g.tty {
			return h, was, err
		}
		msg, _ := errText(err)
		fmt.Fprintf(g.out, "%s.\n", sentenceCase(msg))
		if name, err = g.ask("Another name: "); err != nil {
			return client.Host{}, nil, err
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
	// No host rm of this name runs from here through the save of the
	// connect, which ends the lock (releaseHost).
	ctx, unlock, err := client.BeginHostCreationContextWithKind(g.ctx, client.HostCreationRequest{Name: name, Provider: kind}, "setup")
	if err != nil {
		return client.Host{}, err
	}
	g.baseCtx = g.ctx
	g.ctx, g.unlockHost = ctx, unlock
	h, err := g.local.Create(g.ctx, kind, name, g.out)
	if err != nil {
		return client.Host{}, err
	}
	g.ctx, err = client.ContinueHostCreationContext(g.ctx)
	if err != nil {
		return client.Host{}, err
	}
	fmt.Fprintf(g.out, "  ✓ created the %s machine %s\n", h.Machine.Provider, h.Machine.ID)
	g.log.Add("made the %s machine %s, and saved the host %s (%s host rm %s --destroy deletes it)", h.Machine.Provider, h.Machine.ID, h.Name, brand.Name, h.Name)
	return h, nil
}

// releaseHost lets go of the lock of the host that the setup made, once.
func (g *guide) releaseHost() {
	if g.unlockHost != nil {
		g.unlockHost()
		g.unlockHost = nil
	}
	if g.baseCtx != nil {
		g.ctx = g.baseCtx
		g.baseCtx = nil
	}
}

// errText is an error's message, and its hint for a user error.
func errText(err error) (string, string) {
	var ue *client.UserError
	if errors.As(err, &ue) {
		return ue.Msg, ue.Hint
	}
	return err.Error(), ""
}
