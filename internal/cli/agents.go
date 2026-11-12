package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func agentsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agents", Short: "List the agents, and install one on a host"}
	cmd.AddCommand(agentsLsCmd(), agentsInstallCmd())
	return cmd
}

func agentsLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the agents in the agent table, with how each one installs",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if asJSON {
				return printJSON(agents.Table)
			}
			for _, a := range agents.Table {
				install := "—"
				if a.Setup != nil && a.Setup.Install != "" {
					install = a.Setup.Install
				}
				fmt.Printf("%-12s %-16s %-12s %s\n", a.Kind, a.Title, a.Support, install)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func agentsInstallCmd() *cobra.Command {
	var hostName string
	var force bool
	cmd := &cobra.Command{
		Use:   "install <agent>",
		Short: "Install an agent on a host with its vendor's installer, in a session you can watch",
		Long: "Runs the agent's official install command in a new session on the host, so you see\n" +
			"its output and can answer its questions. Then it says how to log in: you run the\n" +
			"agent's own login, in a session. " + brand.DisplayName + " never logs in for you.",
		Example: "  " + brand.Name + " agents install claude --host my-vps",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, ok := agents.Find(args[0])
			if !ok || a.Setup == nil || a.Setup.Install == "" {
				return &client.UnknownNameError{
					UserError: &client.UserError{Msg: fmt.Sprintf("there is no install command for %q", args[0]), Hint: brand.Name + " agents ls", Code: "agent_not_found", Kind: client.KindNotFound},
					Name:      args[0], Known: installable(),
				}
			}
			h, err := pickHost(hostName)
			if err != nil {
				return err
			}
			return installAgent(cmd.Context(), h, a, force)
		},
	}
	cmd.Flags().StringVar(&hostName, "host", "", "the host (needed when you have more than one)")
	cmd.Flags().BoolVar(&force, "force", false, "run the installer even when the agent is installed, for example to update it")
	return cmd
}

// installable lists the agents that agents install can install.
func installable() []string {
	all := agents.Installable()
	out := make([]string, len(all))
	for i, a := range all {
		out[i] = a.Kind
	}
	return out
}

// pickHost is the named host, or the only one.
func pickHost(name string) (client.Host, error) {
	if name != "" {
		h, err := client.FindHost(name)
		return h, fromFlag(err, "host")
	}
	hosts, err := client.LoadHosts()
	if err != nil {
		return client.Host{}, err
	}
	switch len(hosts) {
	case 0:
		return client.Host{}, &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
	case 1:
		return hosts[0], nil
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	return client.Host{}, &client.UserError{Msg: "which host? You have " + strings.Join(names, ", "), Hint: "Add --host <name>.", Code: "host_needed", Kind: client.KindUsage}
}

func installAgent(ctx context.Context, h client.Host, a agents.Agent, force bool) error {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	conn, err := client.Dial(cctx, h, true)
	if err != nil {
		return err
	}
	checks, err := conn.AgentChecks(cctx)
	if err != nil {
		conn.Close()
		return err
	}
	if c, ok := findCheck(checks, a.Kind); ok && c.Installed && !force {
		conn.Close()
		fmt.Printf("%s is already installed on %s, at %s.\n", a.Title, h.Name, client.CleanPath(c.Path))
		printAgentChecks(os.Stdout, h.Name, []api.AgentCheck{c})
		fmt.Printf("\nTo run its installer again, for example to update it, add --force.\n")
		return nil
	}
	fmt.Printf("Installing %s on %s with its vendor's installer:\n\n    %s\n\n", a.Title, h.Name, a.Setup.Install)
	fmt.Printf("It comes from %s.\nIt runs in a session, so you see its output and can answer its questions.\n\n", a.Setup.Source)
	cols, rows := termSize()
	args := client.TerminalArgs(onboard.InstallArgs(a), cols, rows)
	s, err := conn.NewSession(cctx, args)
	conn.Close()
	if err != nil {
		return err
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		target := client.CommandTarget(h.Name, s.ID)
		fmt.Printf("Started %s. Attach to answer its questions: %s attach %s\n", target, brand.Name, target)
		return nil
	}
	ended, err := attachEnded(ctx, h, s.ID, s.Name)
	if err != nil {
		return err
	}
	if !ended {
		fmt.Printf("The installer keeps running. When it is done, run `%s host doctor %s`.\n", brand.Name, h.Name)
		return nil
	}
	return afterInstall(ctx, h, a)
}

// afterInstall checks the agent again and says how to log in.
func afterInstall(ctx context.Context, h client.Host, a agents.Agent) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(cctx, h, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	checks, err := conn.AgentChecks(cctx)
	if err != nil {
		return err
	}
	c, ok := findCheck(checks, a.Kind)
	if !ok || !c.Installed {
		return &client.UserError{Msg: fmt.Sprintf("%s is still not installed on %s", a.Title, h.Name),
			Hint: "See " + a.Setup.Source + ", or read the installer's output above.", Code: "install_failed"}
	}
	fmt.Println()
	printAgentChecks(os.Stdout, h.Name, []api.AgentCheck{c})
	if c.Login != api.LoginYes {
		fmt.Printf("\nNext, log in to %s with its own login, in a session on %s:\n\n    %s\n", a.Title, h.Name, loginCommand(h.Name, a))
	}
	return nil
}

func findCheck(checks []api.AgentCheck, kind string) (api.AgentCheck, bool) {
	for _, c := range checks {
		if c.Kind == kind {
			return c, true
		}
	}
	return api.AgentCheck{}, false
}

// loginCommand is the command that starts an agent's own login in a new
// session on host.
func loginCommand(host string, a agents.Agent) string { return onboard.LoginCommand(host, a) }

// printAgentChecks prints one line for each agent, and what to do next.
func printAgentChecks(w io.Writer, host string, checks []api.AgentCheck) {
	for _, c := range client.CleanAgentChecks(checks) {
		printLine(w, onboard.Describe(host, c))
	}
}

// printLine prints one agent's line of a summary, and what to try.
func printLine(w io.Writer, l onboard.Line) {
	fmt.Fprintf(w, "  %s %-16s %s\n", l.Mark, l.Title, l.State)
	if l.Try != "" {
		fmt.Fprintf(w, "    Try: %s\n", l.Try)
	}
}
