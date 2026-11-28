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

func newCmd() *cobra.Command {
	var agent, dir, name, base, memoryMax, wallLevel string
	var detach, worktree, inline, setup, noSetup, mkdir, asJSON bool
	var secrets []string
	cmd := &cobra.Command{
		Use:   "new <host> [-- <command>…]",
		Short: "Start a session on a host, and attach to it",
		Example: "  " + brand.Name + " new my-vps --agent claude --dir ~/src/app\n" +
			"  " + brand.Name + " new my-vps --agent claude --wall process --dir ~/src/app\n" +
			"  " + brand.Name + " new my-vps --agent claude --dir ~/src/new-app --mkdir\n" +
			"  " + brand.Name + " new my-vps --agent claude --secret ANTHROPIC_API_KEY\n" +
			"  " + brand.Name + " new my-vps --agent claude --memory-max 6G\n" +
			"  " + brand.Name + " new my-vps --detach -- htop\n" +
			"  " + brand.Name + " new my-vps --agent claude --detach --json",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON && !detach {
				return needsDetach(cmd)
			}
			wl, err := parseWallFlag(wallLevel)
			if err != nil {
				return err
			}
			wallLevel = wl
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			if setup && noSetup {
				return &client.UserError{Msg: "--setup and --no-setup cannot go together", Hint: "Pass one of them.", Code: "flags_conflict", Kind: client.KindUsage}
			}
			var memMax int64
			if cmd.Flags().Changed("memory-max") {
				if memMax, err = client.ParseMemoryFlag(memoryMax); err != nil {
					return err
				}
			}
			// Fetch the secrets first: a password manager may want a touch,
			// and a failing command should not cost a connection.
			values, err := client.FetchSecrets(cmd.Context(), client.LoadSettings(), client.SecretNames(h, secrets), secretTerminal(cmd))
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
			a := client.TerminalArgs(client.NewArgs{Agent: agent, Cwd: client.RemotePath(dir), MakeFolder: mkdir, Name: name, Worktree: worktree, Base: base, Inline: inline, MemoryMax: memMax, Secrets: values, Wall: wallLevel}, cols, rows)
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				a.Argv = args[dash:]
			}
			if setup || noSetup {
				a.Setup = setupValue(setup)
			}
			cancel()
			s, err := startSession(cmd, conn, h, a)
			if err != nil {
				conn.Close()
				return err
			}
			if !detach {
				conn.Close()
				return attach(cmd.Context(), h, s.ID, s.Name)
			}
			if asJSON {
				defer conn.Close()
				return printSession(cmd, conn, h, *s)
			}
			conn.Close()
			d := client.CleanSession(*s)
			say(cmd, "Started %s/%s (%s, %s).\n", h.Name, d.ID, d.Name, d.Agent)
			return nil
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
	cmd.Flags().StringVar(&wallLevel, "wall", "", "sandbox wall level: none or process")
	addJSON(cmd, &asJSON)
	return cmd
}

func parseWallFlag(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	w := strings.ToLower(strings.TrimSpace(raw))
	switch w {
	case "none", "process", "plain":
		return w, nil
	default:
		return "", &client.UserError{
			Msg:  fmt.Sprintf("--wall: unknown wall level %q", raw),
			Hint: "Pass none or process.",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
}

// listSessions asks a host for its sessions. Tests replace it.
var listSessions = func(ctx context.Context, conn *client.Conn) ([]client.Session, error) { return conn.Sessions(ctx) }

// printSession prints a session that a command started, as JSON (cli-spec
// 7.1). It asks the host for its list, so that the address uses the ID when
// the name could mean another session. The session has started, so a list
// that does not come is no failure: the address then uses the ID, and a
// warning says why.
func printSession(cmd *cobra.Command, conn *client.Conn, h client.Host, s client.Session) error {
	lctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	list, err := listSessions(lctx, conn)
	obj := sessionObject(h.Name, s, list)
	if err != nil {
		obj["address"] = h.Name + "/" + s.ID
		note(cmd, "address_by_id", fmt.Sprintf("%s/%s started, but %s did not list its sessions (%v), so the address uses the ID.", h.Name, s.ID, h.Name, err))
	}
	return printJSON(obj)
}

// resolve finds a session by ID or by unique name.
func resolve(ctx context.Context, conn *client.Conn, ref string) (*client.Session, error) {
	list, err := conn.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	return resolveIn(list, ref, conn.Host.Name)
}

// resolveIn finds a session of host by ID or by unique name in list.
func resolveIn(list []client.Session, ref, host string) (*client.Session, error) {
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
		return nil, &client.UserError{Msg: fmt.Sprintf("no session %q on %s", ref, host), Hint: brand.Name + " ls --host " + host, Code: "session_not_found", Kind: client.KindNotFound}
	default:
		return nil, &client.UserError{Msg: fmt.Sprintf("%d sessions are named %q on %s", len(byName), ref, host), Hint: "Use the session ID from `" + brand.Name + " ls`.", Code: "session_ambiguous", Kind: client.KindUsage}
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

func killCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "kill <host>/<session>",
		Aliases: []string{"rm", "delete"},
		Short:   "Stop a session and remove it",
		Example: "  " + brand.Name + " kill my-vps/auth-fix",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				res, err := conn.Delete(ctx, s.ID)
				if err != nil {
					return err
				}
				d := client.CleanSession(*s)
				say(cmd, "Deleted %s (%s) on %s.\n", d.Name, d.ID, h.Name)
				// WorktreeNote cleans the note and the hint (P0.4).
				note, hint := client.WorktreeNote(h.Name, res)
				if note != "" {
					say(cmd, "%s\n", strings.ToUpper(note[:1])+note[1:]+".")
				}
				if hint != "" {
					say(cmd, "Try: %s\n", hint)
				}
				return nil
			})
		},
	}
}

func renameCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rename <host>/<session> <name>",
		Short:   "Rename a session",
		Example: "  " + brand.Name + " rename my-vps/auth-fix login-fix",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				if err := conn.RenameSession(ctx, s.ID, args[1]); err != nil {
					return err
				}
				say(cmd, "Renamed %s to %s on %s.\n", client.CleanLine(s.Name), args[1], h.Name)
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
}

// rememberLists writes the lists of the hosts that answered to the session
// cache, so that the picker and the launcher draw them at once.
func rememberLists(results []hostSessions) {
	lists := map[string][]client.Session{}
	for _, r := range results {
		if r.err == nil {
			lists[r.Host] = r.Sessions
		}
	}
	tui.RememberSessions(lists)
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
			list, err := conn.Sessions(ctx)
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
		Use:     "ls",
		Short:   "List sessions on every host",
		Example: "  " + brand.Name + " ls\n  " + brand.Name + " ls --host my-vps --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			if hostName != "" {
				h, err := client.FindHost(hostName)
				if err != nil {
					return fromFlag(err, "host")
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
	cmd.Flags().StringVar(&hostName, "host", "", "only this host")
	addJSON(cmd, &asJSON)
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
				say(cmd, "Sent to %s on %s.\n", client.CleanLine(s.Name), h.Name)
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
