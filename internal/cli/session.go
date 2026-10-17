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
	"github.com/iamlakshya/simstim/internal/onboard"
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

func newCmd() *cobra.Command {
	var agent, dir, name, base, memoryMax string
	var detach, worktree, inline, setup, noSetup, mkdir bool
	var secrets []string
	cmd := &cobra.Command{
		Use:   "new <host> [-- command…]",
		Short: "Start a session on a host, and attach to it",
		Example: "  " + brand.Name + " new my-vps --agent claude --dir ~/src/app\n" +
			"  " + brand.Name + " new my-vps --agent claude --dir ~/src/new-app --mkdir\n" +
			"  " + brand.Name + " new my-vps --agent claude --secret ANTHROPIC_API_KEY\n" +
			"  " + brand.Name + " new my-vps --agent claude --memory-max 6G\n" +
			"  " + brand.Name + " new my-vps --detach -- htop",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			if setup && noSetup {
				return &client.UserError{Msg: "--setup and --no-setup cannot go together", Hint: "Pass one of them."}
			}
			var memMax int64
			if cmd.Flags().Changed("memory-max") {
				if memMax, err = client.ParseMemoryFlag(memoryMax); err != nil {
					return err
				}
			}
			// Fetch the secrets first: a password manager may want a touch,
			// and a failing command should not cost a connection.
			values, err := client.FetchSecrets(cmd.Context(), client.LoadSettings(), client.SecretNames(h, secrets), isTerminal(os.Stdin))
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			conn, err := client.Dial(ctx, h, true)
			if err != nil {
				cancel()
				return err
			}
			cols, rows := termSize()
			a := client.TerminalArgs(client.NewArgs{Agent: agent, Cwd: client.RemotePath(dir), MakeFolder: mkdir, Name: name, Worktree: worktree, Base: base, Inline: inline, MemoryMax: memMax, Secrets: values}, cols, rows)
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				a.Argv = args[dash:]
			}
			if setup || noSetup {
				a.Setup = setupValue(setup)
			}
			cancel()
			s, err := startSession(cmd.Context(), conn, h, a)
			conn.Close()
			if err != nil {
				return err
			}
			if detach {
				fmt.Printf("Started %s/%s (%s, %s).\n", h.Name, s.ID, s.Name, s.Agent)
				return nil
			}
			return attach(cmd.Context(), h, s.ID, s.Name)
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "shell", "the agent: "+agentKinds())
	cmd.Flags().StringVarP(&dir, "dir", "d", "~", "the folder on the host")
	cmd.Flags().BoolVar(&mkdir, "mkdir", false, "make the folder on the host if it does not exist, without asking")
	cmd.Flags().StringVarP(&name, "name", "n", "", "a name for the session (default: the folder name)")
	cmd.Flags().BoolVar(&detach, "detach", false, "start it without attaching")
	cmd.Flags().BoolVarP(&worktree, "worktree", "w", false, "give the session its own git worktree and branch")
	cmd.Flags().BoolVar(&inline, "inline", false, "start the agent in its inline mode, so your terminal keeps its scrollback (see agents.inline in config.toml)")
	cmd.Flags().StringVar(&base, "base", "", "the commit or branch the worktree starts from (default: HEAD)")
	cmd.Flags().StringVar(&memoryMax, "memory-max", "", "cap the session's memory: auto, or a `size` such as 4G (default: sessions.memory_max in config.toml)")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "pass a secret from config.toml's [secrets] into the session's environment (repeatable)")
	cmd.Flags().BoolVar(&setup, "setup", false, "run the repository's setup script this time, whatever your answer for it")
	cmd.Flags().BoolVar(&noSetup, "no-setup", false, "do not run the repository's setup script this time")
	return cmd
}

// attach runs a session in this terminal.
func attach(ctx context.Context, h client.Host, id, name string) error {
	_, err := attachEnded(ctx, h, id, name)
	return err
}

// attachEnded runs a session in this terminal, and reports whether the
// session ended (rather than the user detaching).
func attachEnded(ctx context.Context, h client.Host, id, name string) (bool, error) {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return false, &client.UserError{Msg: "attach needs a terminal", Hint: "Run it from a terminal, not a pipe."}
	}
	tui.RememberLast(h.Name, id, name)
	res, err := client.Attach(ctx, client.AttachOptions{
		Host: h, Session: id, Settings: client.LoadSettings(), Interactive: true,
	})
	if err != nil {
		return false, err
	}
	if res.Ended != nil {
		fmt.Print(endMessage(name, h.Name, id, res.Ended))
	} else {
		fmt.Printf("\r\nDetached. %s keeps running on %s.\n", name, h.Name)
	}
	return res.Ended != nil, nil
}

// resolve finds a session by ID or by unique name.
func resolve(ctx context.Context, conn *client.Conn, ref string) (*client.Session, error) {
	list, err := conn.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	var byName []client.Session
	for _, s := range list {
		if s.ID == ref {
			return &s, nil
		}
		if s.Name == ref {
			byName = append(byName, s)
		}
	}
	switch len(byName) {
	case 1:
		return &byName[0], nil
	case 0:
		return nil, &client.UserError{Msg: fmt.Sprintf("no session %q on %s", ref, conn.Host.Name), Hint: brand.Name + " ls --host " + conn.Host.Name}
	default:
		return nil, &client.UserError{Msg: fmt.Sprintf("%d sessions are named %q on %s", len(byName), ref, conn.Host.Name), Hint: "Use the session ID from `" + brand.Name + " ls`."}
	}
}

func withSession(ctx context.Context, target string, fn func(context.Context, client.Host, *client.Conn, *client.Session) error) error {
	hn, ref, err := client.SplitTarget(target)
	if err != nil {
		return err
	}
	h, err := client.FindHost(hn)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(cctx, h, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	s, err := resolve(cctx, conn, ref)
	if err != nil {
		return err
	}
	return fn(cctx, h, conn, s)
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "attach <host>/<session> | -",
		Aliases: []string{"a"},
		Short:   "Attach to a session (detach with Ctrl+\\); - is the last one",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if target == "-" {
				host, id, ok := tui.LastSession()
				if !ok {
					return &client.UserError{Msg: "no session has been attached yet", Hint: brand.Name + " attach <host>/<session>, or pick one in " + brand.Name}
				}
				target = host + "/" + id
			}
			var h client.Host
			var s *client.Session
			err := withSession(cmd.Context(), target, func(_ context.Context, hh client.Host, _ *client.Conn, ss *client.Session) error {
				h, s = hh, ss
				return nil
			})
			if err != nil {
				return err
			}
			if client.Ended(*s) {
				return endedError(h, *s)
			}
			return attach(cmd.Context(), h, s.ID, s.Name)
		},
	}
}

func killCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "kill <host>/<session>",
		Aliases: []string{"rm", "delete"},
		Short:   "Stop a session and remove it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				res, err := conn.Delete(ctx, s.ID)
				if err != nil {
					return err
				}
				fmt.Printf("Deleted %s (%s) on %s.\n", s.Name, s.ID, h.Name)
				if res.Worktree != "" {
					fmt.Println(strings.ToUpper(res.Worktree[:1]) + res.Worktree[1:] + ".")
				}
				return nil
			})
		},
	}
}

func renameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <host>/<session> <new-name>",
		Short: "Rename a session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				if err := conn.RenameSession(ctx, s.ID, args[1]); err != nil {
					return err
				}
				fmt.Printf("Renamed %s to %s on %s.\n", s.Name, args[1], h.Name)
				return nil
			})
		},
	}
}

type hostSessions struct {
	Host     string           `json:"host"`
	Sessions []client.Session `json:"sessions"`
	Error    string           `json:"error,omitempty"`
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
				results[i].Error = err.Error()
				return
			}
			defer conn.Close()
			list, err := conn.Sessions(ctx)
			if err != nil {
				results[i].Error = err.Error()
			}
			results[i].Sessions = list
		}()
	}
	wg.Wait()
	return results
}

// printSessions prints one line for each session, by host. The state column
// is as wide as its longest state, so a cause such as "(out of memory)"
// keeps the columns after it in line.
func printSessions(results []hostSessions) {
	sort.Slice(results, func(i, j int) bool { return results[i].Host < results[j].Host })
	stateW := 12
	for _, r := range results {
		for _, s := range r.Sessions {
			stateW = max(stateW, len([]rune(stateLabel(s))))
		}
	}
	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("%s  ◌ %s\n", r.Host, r.Error)
			continue
		}
		if len(r.Sessions) == 0 {
			fmt.Printf("%s  no sessions\n", r.Host)
			continue
		}
		for _, s := range r.Sessions {
			fmt.Printf("%-14s %-10s %-20s %-9s %-*s %6s  %s\n", r.Host, s.ID, trunc(s.Name, 20), s.Agent, stateW, stateLabel(s), age(s.CreatedAt), s.Cwd)
		}
	}
}

func lsCmd() *cobra.Command {
	var hostName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List sessions on every host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			if hostName != "" {
				h, err := client.FindHost(hostName)
				if err != nil {
					return err
				}
				hosts = []client.Host{h}
			}
			if len(hosts) == 0 {
				return &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>"}
			}
			results := listAll(cmd.Context(), hosts)
			if asJSON {
				return printJSON(results)
			}
			printSessions(results)
			return nil
		},
	}
	cmd.Flags().StringVar(&hostName, "host", "", "only this host")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
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
		Use:     "send <host>/<session> <text>",
		Short:   "Type text into a session without attaching, then press Enter",
		Example: "  " + brand.Name + " send my-vps/auth-fix \"run the tests and fix what fails\"",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				if err := conn.SendText(ctx, s.ID, args[1], !noEnter); err != nil {
					return err
				}
				fmt.Printf("Sent to %s on %s.\n", s.Name, h.Name)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&noEnter, "no-enter", false, "do not press Enter after the text")
	return cmd
}

func flipCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "flip [<host>/<session>]",
		Short: "Switch sessions quickly: the session list of every host, then attach",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return attachCmd().RunE(cmd, args)
			}
			return tui.RunWith(cmd.Context(), tui.Options{Switcher: true, Local: onboard.System{Providers: providers}})
		},
	}
}

func demoCmd() *cobra.Command {
	var empty bool
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Try the launcher with sample hosts and sessions (nothing connects anywhere)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
