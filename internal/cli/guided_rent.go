package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// rent walks through renting a server, one step at a time, as the tutorial
// does, and returns the host it names. It checks each step it can, and it
// never rents anything or edits ~/.ssh/config without a yes.
func (g *guide) rent() (client.Host, error) {
	r := onboard.RentPlan{User: onboard.RentUser, Name: onboard.RentName}
	step := func(n int) {
		fmt.Fprintf(g.out, "\nStep %d of %d: %s\n\n", n+1, len(onboard.RentTitles), onboard.RentTitles[n])
	}

	step(onboard.RentKey)
	if err := g.rentKey(&r); err != nil {
		return client.Host{}, err
	}

	step(onboard.RentServer)
	g.steps(r.ServerSteps())
	fmt.Fprintln(g.out)
	for r.Addr == "" {
		a, err := g.ask("The server's address, such as 203.0.113.10: ")
		if err != nil {
			return client.Host{}, err
		}
		if onboard.ValidAddr(a) {
			r.Addr = a
		} else if a != "" {
			fmt.Fprintln(g.out, "Type the address the console shows: an IP address or a DNS name.")
		}
	}

	step(onboard.RentUserStep)
	for {
		a, err := g.ask(fmt.Sprintf("The user's name for your agents [%s]: ", onboard.RentUser))
		if err != nil {
			return client.Host{}, err
		}
		if a == "" || onboard.ValidUser(a) {
			if a != "" {
				r.User = a
			}
			break
		}
		fmt.Fprintln(g.out, "Use lower-case letters, digits, dashes and underscores.")
	}
	fmt.Fprintln(g.out)
	g.steps(r.UserSteps())
	if err := g.until("ssh "+r.Login()+" works", func() error { return g.local.SSHWorks(g.ctx, r.Login()) },
		"\nPress Enter when it is done, to check that ssh "+r.Login()+" works: "); err != nil {
		return client.Host{}, err
	}

	step(onboard.RentNameStep)
	if err := g.rentName(&r); err != nil {
		return client.Host{}, err
	}
	return client.Host{Name: r.Name, SSH: r.Name}, nil
}

// rentKey finds the user's SSH key, or offers to make one.
func (g *guide) rentKey(r *onboard.RentPlan) error {
	for {
		r.Key = g.local.PublicKey()
		g.steps(r.KeySteps())
		if r.Key != "" {
			return nil
		}
		fmt.Fprintln(g.out)
		ok, err := g.yesNo("Run it now?", false)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := g.ask("Make it yourself, then press Enter to check again: "); err != nil {
				return err
			}
			continue
		}
		if err := g.ctx.Err(); err != nil {
			// A Ctrl+C that came with the yes: make no key.
			return err
		}
		cmd := exec.CommandContext(g.ctx, onboard.KeyArgs[0], onboard.KeyArgs[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if g.ctx.Err() != nil {
				return g.ctx.Err()
			}
			fmt.Fprintf(g.out, "  ✕ ssh-keygen: %v\n", err)
		} else if g.local.PublicKey() != "" {
			g.log.Add("made an SSH key in ~/.ssh")
		}
		fmt.Fprintln(g.out)
	}
}

// rentName names the server in ~/.ssh/config, after a yes, and checks that
// the name reaches it.
func (g *guide) rentName(r *onboard.RentPlan) error {
	for {
		a, err := g.ask(fmt.Sprintf("A name for the server [%s]: ", onboard.RentName))
		if err != nil {
			return err
		}
		if a == "" {
			a = onboard.RentName
		}
		if !client.ValidHostName(a) {
			fmt.Fprintln(g.out, "Use letters, digits, dots, dashes and underscores.")
			continue
		}
		if _, err := client.FindHost(a); err == nil {
			fmt.Fprintf(g.out, "There is already a host called %s in %s. Pick another name.\n", a, brand.DisplayName)
			continue
		}
		r.Name = a
		break
	}
	first := ""
	if g.local.HasSSHHost(r.Name) {
		fmt.Fprintf(g.out, "\nYour ~/.ssh/config already names %s. This checks that the name reaches the server.\n", r.Name)
	} else {
		fmt.Fprintf(g.out, "\nThese lines give the server its name in ~/.ssh/config:\n\n")
		for _, l := range strings.Split(strings.TrimRight(r.ConfigLines(), "\n"), "\n") {
			fmt.Fprintf(g.out, "    %s\n", l)
		}
		fmt.Fprintln(g.out)
		ok, err := g.yesNo("Add them to the end of ~/.ssh/config?", false)
		if err != nil {
			return err
		}
		if ok {
			if err := g.ctx.Err(); err != nil {
				// A Ctrl+C that came with the yes: write nothing.
				return err
			}
			if err := g.local.AddSSHHost(r.ConfigLines()); err != nil {
				return &client.UserError{Msg: "could not add the lines to ~/.ssh/config: " + err.Error(), Hint: "Add them yourself, then run " + brand.Name + " setup --host " + shellWord(r.Name) + ".", Code: "ssh_config_write"}
			}
			fmt.Fprintf(g.out, "  ✓ added %s to ~/.ssh/config\n", r.Name)
			g.log.Add("added %s to ~/.ssh/config", r.Name)
		} else {
			first = "Add them yourself, then press Enter to check: "
		}
	}
	return g.until("ssh "+r.Name+" works", func() error { return g.local.SSHWorks(g.ctx, r.Name) }, first)
}

// until runs check until it passes. Before each try after a failure, the
// user presses Enter, or types s to go on without it. first, if set, is
// asked before the first try.
func (g *guide) until(what string, check func() error, first string) error {
	q := first
	for {
		if q != "" {
			a, err := g.ask(q)
			if err != nil {
				return err
			}
			if q != first && strings.EqualFold(a, "s") {
				fmt.Fprintf(g.out, "  Going on without the check. If the host does not answer, run: %s setup --host <name>\n", brand.Name)
				return nil
			}
		}
		err := check()
		if g.ctx.Err() != nil {
			return g.ctx.Err()
		}
		if err == nil {
			fmt.Fprintf(g.out, "  ✓ %s\n", what)
			return nil
		}
		msg, hint := errText(err)
		fmt.Fprintf(g.out, "  ✕ %s\n", sentenceCase(msg))
		if hint != "" {
			fmt.Fprintf(g.out, "    Try: %s\n", hint)
		}
		q = "Press Enter to check again, or s to go on without it: "
	}
}
