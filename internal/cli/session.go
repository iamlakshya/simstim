package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/tui"
)

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func termSize() (int, int) {
	c, r, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80, 24
	}
	return c, r
}

// listSessions asks a host for its sessions. Tests replace it.
var listSessions = func(ctx context.Context, conn *client.Conn) ([]client.Session, error) { return conn.Sessions(ctx) }

// oneHost is the argument check of new: one host, then any command after
// "--" (isolation spec 14.14). A second word before "--" fails, so that an
// old "-d <folder>" after its end, or a command without its "--", never
// starts a session that the user did not mean.
func oneHost(c *cobra.Command, args []string) error {
	before := len(args)
	if dash := c.ArgsLenAtDash(); dash >= 0 {
		before = dash
	}
	switch {
	case before == 0:
		return argsError(c, nil)
	case before > 1:
		// A word that no host could have is not repeated (cli-spec 4.2).
		hint := brand.Name + " host ls"
		if client.ValidHostName(args[0]) {
			hint = brand.Name + " new " + args[0] + " -- htop (a folder goes after -C)"
		}
		return &client.UserError{Msg: "new takes one host. A command goes after --", Hint: hint, Code: "extra_argument", Kind: client.KindUsage}
	}
	return nil
}

func renameCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "rename <host>/<session> <name>",
		Short:             "Rename a session",
		Example:           "  " + brand.Name + " rename my-vps/auth-fix login-fix",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[1])
			if name == "" {
				return &client.UserError{Msg: "a name is needed", Hint: "Pass a non-empty name for the session.", Code: "bad_name", Kind: client.KindUsage}
			}
			if err := client.CheckSessionName(name); err != nil {
				return err
			}
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				if err := conn.RenameSession(ctx, s.ID, name); err != nil {
					return err
				}
				renamed := *s
				renamed.Name = name
				client.RememberSession(h.Name, renamed)
				say(cmd, "Renamed %s to %s on %s.\n", shown(s.Name), shown(name), shown(h.Name))
				return nil
			})
		},
	}
}

type hostSessions struct {
	Host     string           `json:"host"`
	Sessions []client.Session `json:"sessions"`
	Error    string           `json:"error,omitempty"`
	// err is the error behind Error.
	err error
	// at is when the host's list arrived, taken in its own worker, so that
	// a slow host does not make another host's list look newer.
	at time.Time
}

// rememberLists writes the lists of the hosts that answered to the session
// cache, so that the picker and the launcher draw them at once. Each list
// keeps the time when it arrived: a list that is older than the cache's
// changes nothing.
func rememberLists(results []hostSessions) {
	lists := map[string]client.HostList{}
	for _, r := range results {
		if r.err == nil {
			lists[r.Host] = client.HostList{Sessions: r.Sessions, At: r.at}
		}
	}
	client.RememberLists(lists)
}

// listAll asks every host for its sessions at once. Listing does not boot
// stopped VMs or resume paused sandboxes.
func listAll(ctx context.Context, hosts []client.Host) []hostSessions {
	results := make([]hostSessions, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].Host = h.Name
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			conn, err := client.Dial(client.WithoutWake(ctx), h, false)
			if err != nil {
				results[i].Error, results[i].err = err.Error(), err
				return
			}
			defer conn.Close()
			list, err := listSessions(ctx, conn)
			results[i].at = time.Now()
			if err != nil {
				results[i].Error, results[i].err = err.Error(), err
			}
			results[i].Sessions = list
		}()
	}
	wg.Wait()
	for i := range results {
		if results[i].Sessions == nil {
			results[i].Sessions = []client.Session{} // a list in JSON, never null
		}
	}
	return results
}

// printSessions prints one line for each session, by host. The state column
// is as wide as its longest state, so a cause such as "(out of memory)"
// keeps the columns after it in line. What a host sent is cleaned first
// (api.CleanText). The JSON keeps the host's own values.
func printSessions(all []hostSessions) {
	results := make([]hostSessions, len(all))
	for i, r := range all {
		results[i] = hostSessions{Host: r.Host, Sessions: client.CleanSessions(r.Sessions), Error: client.CleanLine(r.Error)}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Host < results[j].Host })
	stateW := 12
	for _, r := range results {
		for _, s := range r.Sessions {
			stateW = max(stateW, len([]rune(shown(stateLabel(s)))))
		}
	}
	for _, r := range results {
		host := shown(r.Host)
		if r.Error != "" {
			fmt.Printf("%s  ◌ %s\n", host, shown(r.Error))
			continue
		}
		if len(r.Sessions) == 0 {
			fmt.Printf("%s  no sessions\n", host)
			continue
		}
		for _, s := range r.Sessions {
			fmt.Printf("%-14s %-10s %-20s %-9s %-*s %6s  %s%s\n", host, shown(s.ID), trunc(shown(client.PolicyDisplayName(s)), 20), shown(s.Agent), stateW, stateLabel(s), age(s.CreatedAt), shown(s.Cwd), sessionRuntimeMarksV1(s)+sessionAccountMarkV1(s))
		}
	}
}

func lsCmd() *cobra.Command {
	var hostName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "ls [<host>]",
		Short:             "List sessions on every host, or on one",
		Example:           "  " + brand.Name + " ls\n  " + brand.Name + " ls my-vps --json",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeLsHost,
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			if len(args) == 1 || hostName != "" {
				h, err := lsHost(args, hostName)
				if err != nil {
					return err
				}
				hosts = []client.Host{h}
			}
			if len(hosts) == 0 {
				return &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
			}
			results := listAll(cmd.Context(), hosts)
			rememberLists(results)
			if asJSON {
				return printJSON(results)
			}
			printSessions(results)
			return nil
		},
	}
	addHostFlag(cmd, &hostName, "only this host, as the argument does")
	addJSON(cmd, &asJSON)
	return cmd
}

// lsHost is the one host that ls lists: its argument, or --host, which
// stays as a kept alias. Two different hosts are a usage error.
func lsHost(args []string, flag string) (client.Host, error) {
	if len(args) == 1 && flag != "" && flag != args[0] {
		hint := brand.Name + " ls " + args[0]
		if !client.ValidHostName(args[0]) {
			hint = brand.Name + " host ls (lists the hosts)"
		}
		return client.Host{}, &client.UserError{Msg: fmt.Sprintf("ls got two hosts: %s and %s", args[0], flag),
			Hint: hint, Code: "two_hosts", Kind: client.KindUsage}
	}
	if len(args) == 1 {
		return client.FindHost(args[0])
	}
	h, err := client.FindHost(flag)
	return h, fromFlag(err, "host")
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func sendCmd() *cobra.Command {
	var noEnter bool
	cmd := &cobra.Command{
		Use:               "send <host>/<session> <text>",
		Short:             "Type text into a session without attaching, then press Enter",
		Example:           "  " + brand.Name + " send my-vps/auth-fix \"run the tests and fix what fails\"",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				if err := conn.SendText(ctx, s.ID, args[1], !noEnter); err != nil {
					return err
				}
				say(cmd, "Sent to %s on %s.\n", shown(nameOf(*s)), shown(h.Name))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&noEnter, "no-enter", false, "do not press Enter after the text")
	return cmd
}

func demoCmd() *cobra.Command {
	var empty bool
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Try the launcher with sample data (nothing connects anywhere)",
		Example: "  " + brand.Name + " demo\n" +
			"  " + brand.Name + " demo --empty   # as on the first run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkLauncher(cmd, "the demo", brand.Name+" demo (run it at a terminal with input on)"); err != nil {
				return err
			}
			return tui.RunDemo(cmd.Context(), empty)
		},
	}
	cmd.Flags().BoolVar(&empty, "empty", false, "start with no hosts, as on the first run")
	return cmd
}

// agentKinds lists the agents for help text, from the agent table: "claude,
// codex, … or shell".
func agentKinds() string {
	kinds := make([]string, 0, len(agents.Table))
	for _, a := range agents.Table {
		kinds = append(kinds, a.Kind)
	}
	if len(kinds) < 2 {
		return strings.Join(kinds, "")
	}
	return strings.Join(kinds[:len(kinds)-1], ", ") + " or " + kinds[len(kinds)-1]
}
