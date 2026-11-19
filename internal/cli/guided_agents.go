package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// agents shows the checklist, installs the ticked agents one after
// another, then offers each login, unless the flow leaves logins out.
func (g *guide) agents(h client.Host, f *onboard.Flow) error {
	g.heading("Agents on " + h.Name)
	if err := g.checklist(f); err != nil {
		return err
	}
	f.Start()
	for {
		a := f.Next()
		var err error
		switch a.Kind {
		case onboard.ActInstall:
			err = g.install(h, f, a)
		case onboard.ActLogin:
			err = g.login(h, f, a)
		default:
			return nil
		}
		if err != nil {
			f.Stop()
			return err
		}
	}
}

// checklist shows every agent with an installer, ticks what --agents names,
// or asks which to install.
func (g *guide) checklist(f *onboard.Flow) error {
	if g.o.agentsSet {
		if err := f.Choose(g.o.agents); err != nil {
			return err
		}
	}
	g.printChecklist(f)
	if !g.o.agentsSet && g.tty && !g.o.yes {
		if err := g.askChecklist(f); err != nil {
			return err
		}
	}
	var names []string
	for _, a := range f.Chosen() {
		names = append(names, a.Title)
	}
	if len(names) == 0 {
		fmt.Fprintln(g.out, "Nothing to install.")
	} else {
		fmt.Fprintf(g.out, "To install: %s.\n", strings.Join(names, ", "))
	}
	return nil
}

func (g *guide) printChecklist(f *onboard.Flow) {
	folded := false
	for i, it := range f.Items {
		if it.BestEffort() && !folded {
			folded = true
			fmt.Fprintln(g.out, "  Best effort, kept working by contributors:")
		}
		tick := "[ ]"
		if it.Chosen {
			tick = "[x]"
		}
		fmt.Fprintf(g.out, "  %2d  %s %-16s %s\n", i+1, tick, it.Agent.Title, it.State())
	}
	fmt.Fprintln(g.out)
}

// askChecklist reads the numbers to tick, again until they are all good.
func (g *guide) askChecklist(f *onboard.Flow) error {
	for {
		a, err := g.ask("Type the numbers to install, such as 2 3, or press Enter for none: ")
		if err != nil {
			return err
		}
		var picks []int
		bad := ""
		for _, w := range strings.FieldsFunc(a, func(r rune) bool { return r == ' ' || r == ',' }) {
			n, err := strconv.Atoi(w)
			if err != nil || n < 1 || n > len(f.Items) {
				bad = w
				break
			}
			picks = append(picks, n-1)
		}
		if bad != "" {
			fmt.Fprintf(g.out, "%q is not a number from 1 to %d.\n", bad, len(f.Items))
			continue
		}
		for _, i := range picks {
			if !f.Items[i].Chosen {
				f.Toggle(i)
			}
		}
		return nil
	}
}

// install runs one agent's official installer in a session, then checks
// the host again.
func (g *guide) install(h client.Host, f *onboard.Flow, a onboard.Action) error {
	if a.N == 1 {
		g.heading("Install")
	}
	fmt.Fprintf(g.out, "%s (%d of %d), with its vendor's installer, from %s:\n\n    %s\n\n", a.Agent.Title, a.N, a.Of, a.Agent.Setup.Source, a.Agent.Setup.Install)
	return g.inSession(h, f, a)
}

// login offers an agent's own login in a session. The product never sees
// it.
func (g *guide) login(h client.Host, f *onboard.Flow, a onboard.Action) error {
	if a.N == 1 {
		g.heading("Log in")
		fmt.Fprintf(g.out, "Each agent logs in its own way, in a session on %s. %s never sees the login.\n\n", h.Name, brand.DisplayName)
	}
	fmt.Fprintf(g.out, "%s is not logged in. Its login:\n\n    %s\n\n", a.Agent.Title, a.Agent.Setup.Login)
	ok, err := g.yesNo("Log in now?", true)
	if err != nil {
		return err
	}
	if !ok {
		f.Skip(a)
		fmt.Fprintf(g.out, "Skipped. Later: %s\n", onboard.LoginCommand(h.Name, a.Agent))
		return nil
	}
	return g.inSession(h, f, a)
}

// inSession runs an install or a login, checks the host again, and says
// how it went.
func (g *guide) inSession(h client.Host, f *onboard.Flow, a onboard.Action) error {
	if g.tty {
		fmt.Fprintf(g.out, "It runs in the session %s on %s. Answer it there; ctrl+\\ leaves it running.\n", a.Args.Name, h.Name)
	}
	id, ended, err := g.session(h, a)
	if err != nil {
		return err
	}
	checks, err := g.checks(h)
	if err != nil {
		return err
	}
	a.Session = id
	f.Done(a, checks, ended)
	fmt.Fprintln(g.out)
	printLine(g.out, f.Line(a.Agent.Kind))
	return nil
}

// done sums up, and gives the command for the first session.
func (g *guide) done(h client.Host, f *onboard.Flow) {
	fmt.Fprintf(g.out, "\nDone. %s is ready.\n\n", h.Name)
	for _, l := range f.Summary() {
		printLine(g.out, l)
	}
	fmt.Fprintf(g.out, "\nStart your first session:\n\n    %s\n\nOr run `%s` to open the launcher.\n", f.FirstSession(), brand.Name)
}

// runSession starts an install or a login in a session on h. On a terminal
// it attaches; without one, it waits for the session to end. It returns the
// session's ID, and whether the session ended.
func (g *guide) runSession(h client.Host, a onboard.Action) (string, bool, error) {
	cctx, cancel := context.WithTimeout(g.ctx, 90*time.Second)
	conn, err := client.Dial(cctx, h, true)
	if err != nil {
		cancel()
		return "", false, err
	}
	cols, rows := termSize()
	s, err := conn.NewSession(cctx, client.TerminalArgs(a.Args, cols, rows))
	conn.Close()
	cancel()
	if err != nil {
		return "", false, err
	}
	var ended bool
	if g.tty {
		ended, err = attachEnded(g.ctx, h, s.ID, s.Name)
	} else {
		d := client.CleanSession(*s) // the host's own text, only printed
		fmt.Fprintf(g.out, "Started %s/%s. Waiting for it to end (%s attach %s shows it)…\n", h.Name, d.Name, brand.Name, client.CommandTarget(h.Name, s.ID))
		ended, err = waitEnded(g.ctx, h, s.ID, 15*time.Minute)
	}
	return s.ID, ended, err
}

// waitEnded waits until a session ends, or for limit. It reports whether
// the session ended.
func waitEnded(ctx context.Context, h client.Host, id string, limit time.Duration) (bool, error) {
	wctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	conn, err := client.Dial(wctx, h, true)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	for {
		list, err := conn.Sessions(wctx)
		if err != nil && ctx.Err() == nil && wctx.Err() == nil {
			return false, err
		}
		for _, s := range list {
			if s.ID == id && (!s.Alive || s.State == api.StateExited || s.State == api.StateErrored || s.State == api.StateLost) {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-wctx.Done():
			return false, nil
		case <-time.After(2 * time.Second):
		}
	}
}

// agentChecks asks the host about its agents again.
func (g *guide) agentChecks(h client.Host) ([]api.AgentCheck, error) {
	cctx, cancel := context.WithTimeout(g.ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(cctx, h, true)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.AgentChecks(cctx)
}
