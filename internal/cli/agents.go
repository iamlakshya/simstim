package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List the agents, install one on a host, and log in to it",
		Example: "  " + brand.Name + " agents ls\n" +
			"  " + brand.Name + " agents install claude my-vps\n" +
			"  " + brand.Name + " agents login claude my-vps",
	}
	cmd.AddCommand(agentsLsCmd(), agentsInstallCmd(), agentsLoginCmd())
	return cmd
}

func agentsLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the agents in the agent table, with how each one installs",
		Example: "  " + brand.Name + " agents ls\n" +
			"  " + brand.Name + " agents ls --json",
		Args: cobra.NoArgs,
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
	addJSON(cmd, &asJSON)
	return cmd
}

func agentsInstallCmd() *cobra.Command {
	var hostName string
	var reinstall, usedOldForce bool
	cmd := &cobra.Command{
		Use:   "install <agent> [<host>]",
		Short: "Install an agent on a host with its official installer",
		Long: "Runs the agent's official install command in a new session on the host, so you see\n" +
			"its output and can answer its questions. Then it names the command that logs in:\n" +
			"`agents login`, which runs the agent's own login in a session. " + brand.DisplayName + " never\n" +
			"logs in for you.",
		Example: "  " + brand.Name + " agents install claude my-vps\n" +
			"  " + brand.Name + " agents install codex my-vps --reinstall   # update it",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if usedOldForce {
				warnOld(cmd, oldInstallForce)
			}
			a, err := findAgent(args[0], installable())
			if err != nil {
				return err
			}
			if a.Setup == nil || a.Setup.Install == "" {
				return &client.UserError{
					Msg:  a.Kind + " has no install command",
					Hint: brand.Name + " agents ls",
					Code: "no_installer",
					Kind: client.KindUsage,
				}
			}
			h, err := agentHost(cmd, args, hostName)
			if err != nil {
				return err
			}
			return installAgent(cmd.Context(), h, a, reinstall, inputOn(cmd))
		},
	}
	addHostFlag(cmd, &hostName, "the host (needed when you have more than one)")
	cmd.Flags().BoolVar(&reinstall, "reinstall", false, "run the installer even when the agent is installed, for example to update it")
	if oldInstallForce != nil {
		addOldFlag(cmd, "reinstall", "force", &usedOldForce, oldInstallForce)
	}
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

// findAgent is the agent of kind. An unknown agent fails with exit code 3,
// with the nearest of known as suggestions (cli-spec 12.2 and 12.3).
func findAgent(kind string, known []string) (agents.Agent, error) {
	if a, ok := agents.Find(kind); ok {
		return a, nil
	}
	return agents.Agent{}, &client.UnknownNameError{
		UserError: &client.UserError{
			Msg:  fmt.Sprintf("there is no agent called %q", kind),
			Hint: brand.Name + " agents ls",
			Code: "agent_not_found",
			Kind: client.KindNotFound,
		},
		Name: kind, Known: known,
	}
}

// agentHost is the host of agents install and agents login: the second
// argument, --host, or the only saved host (cli-spec 12.1).
func agentHost(cmd *cobra.Command, args []string, flag string) (client.Host, error) {
	arg := ""
	if len(args) > 1 {
		arg = args[1]
	}
	switch {
	case arg != "" && flag != "" && arg != flag:
		return client.Host{}, &client.UserError{
			Msg:  fmt.Sprintf("%s got two hosts: %s, and %s with --host", shortPath(cmd), arg, flag),
			Hint: "Name the host once: as the second word, or with --host.",
			Code: "host_conflict",
			Kind: client.KindUsage,
		}
	case arg != "":
		h, err := client.FindHost(arg)
		return h, fromArg(err, 1)
	case flag != "":
		h, err := client.FindHost(flag)
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
	return client.Host{}, &client.UserError{
		Msg:  fmt.Sprintf("%s needs a host: you have %s", shortPath(cmd), strings.Join(names, ", ")),
		Hint: withHostWord(cmd, args[0], names[0]),
		Code: "host_needed",
		Kind: client.KindUsage,
	}
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

// fromArg says that the unknown name of err, if it has one, is the
// argument at index i.
func fromArg(err error, i int) error {
	var e *client.UnknownNameError
	if errors.As(err, &e) {
		e.Arg = i
	}
	return err
}

// withHostWord is the command line of cmd with host after agent, the first
// word after the command's path: a command that runs as written.
func withHostWord(cmd *cobra.Command, agent, host string) string {
	line := commandLine(cmd)
	at, skip := plain(cmd, line), pathLen(cmd)
	if len(at) <= skip {
		return brand.Name + " " + shortPath(cmd) + " " + joinWords([]string{agent, host})
	}
	i := at[skip] + 1
	out := append(slices.Clone(line[:i]), host)
	return brand.Name + " " + joinWords(append(out, line[i:]...))
}

// installAgent runs an agent's installer in a session on h. With attachOK,
// which input on gives, it attaches, so the user answers the installer.
// Otherwise it starts the session, and says how to attach to it.
func installAgent(ctx context.Context, h client.Host, a agents.Agent, reinstall, attachOK bool) error {
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
	if c, ok := findCheck(checks, a.Kind); ok && c.Installed && !reinstall {
		conn.Close()
		c = cleanCheck(a, c)
		fmt.Fprintf(os.Stderr, "%s is already installed on %s, at %s.\n", a.Title, h.Name, c.Path)
		printAgentChecks(os.Stderr, h.Name, []api.AgentCheck{c})
		fmt.Fprintf(os.Stderr, "\nTo run its installer again, for example to update it, add --reinstall.\n")
		return nil
	}
	fmt.Fprintf(os.Stderr, "Installing %s on %s with its vendor's installer:\n\n    %s\n\n", a.Title, h.Name, a.Setup.Install)
	fmt.Fprintf(os.Stderr, "It comes from %s.\nIt runs in a session, so you see its output and can answer its questions.\n\n", a.Setup.Source)
	cols, rows := termSize()
	args := client.TerminalArgs(onboard.InstallArgs(a), cols, rows)
	s, err := conn.NewSession(cctx, args)
	conn.Close()
	if err != nil {
		return err
	}
	if !attachOK {
		target := client.CommandTarget(h.Name, s.ID)
		fmt.Fprintf(os.Stderr, "Started %s. Attach to answer its questions: %s attach %s\n", target, brand.Name, target)
		return nil
	}
	ended, stuck, err := attachSession(ctx, h, s.ID, s.Name)
	if err != nil {
		return err
	}
	if !ended {
		fmt.Fprintf(os.Stderr, "The installer keeps running. When it is done, run `%s host doctor %s`.\n", brand.Name, h.Name)
		return nil
	}
	return afterInstall(ctx, h, a, !stuck)
}

// afterInstall checks the agent again and says how to log in.
func afterInstall(ctx context.Context, h client.Host, a agents.Agent, interactive bool) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(cctx, h, interactive)
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
		return &client.UserError{
			Msg:  fmt.Sprintf("%s is still not installed on %s", a.Title, h.Name),
			Hint: "See " + a.Setup.Source + ", or read the installer's output above.",
			Code: "install_failed",
		}
	}
	// The line after the install names agents login (cli-spec 12.2, rule 4),
	// so the agent's own line does not repeat it.
	c = cleanCheck(a, c)
	l, next := onboard.Describe(h.Name, c), ""
	if c.OnPath && c.Login != api.LoginYes {
		next = onboard.LoginCommand(h.Name, a)
	}
	if next != "" {
		l.Try = ""
	}
	fmt.Fprintln(os.Stderr)
	printLine(os.Stderr, l)
	rebuildAgentsDisk(ctx, conn)
	if next != "" {
		fmt.Fprintf(os.Stderr, "\nNext, log in: %s\n", next)
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

// printAgentChecks prints one line for each agent, and what to do next.
func printAgentChecks(w io.Writer, host string, checks []api.AgentCheck) {
	for _, c := range client.CleanAgentChecks(checks) {
		printLine(w, onboard.Describe(host, c))
	}
}

// printLine prints one agent's line of a summary, and what to try. The
// line holds the host's text, a title, a path and a reason, so it prints
// cleaned.
func printLine(w io.Writer, l onboard.Line) {
	fmt.Fprintf(w, "  %s %-16s %s\n", l.Mark, client.CleanLine(l.Title), client.CleanLine(l.State))
	if l.Try != "" {
		fmt.Fprintf(w, "    Try: %s\n", client.CleanLine(l.Try))
	}
}
