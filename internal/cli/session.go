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

type newOpts struct {
	agent, dir, name, base, wallLevel                       string
	detach, worktree, inline, setup, noSetup, mkdir, asJSON bool
	usedOldDir, usedOldSetup, usedOldNoSetup                bool
	secrets, expose                                         []string
	p114                                                    newP114Opts
}

func newCmd() *cobra.Command {
	var o newOpts
	cmd := &cobra.Command{
		Use:   "new <host> [-- <command>…]",
		Short: "Start a session on a host, and attach to it",
		Example: "  " + brand.Name + " new my-vps --agent claude --dir ~/src/app\n" +
			"  " + brand.Name + " new my-vps --agent claude --wall process --dir ~/src/app\n" +
			"  " + brand.Name + " new my-vps --agent claude --dir ~/src/new-app --mkdir\n" +
			"  " + brand.Name + " new my-vps --agent claude --secret ANTHROPIC_API_KEY\n" +
			"  " + brand.Name + " new my-vps --agent claude --memory 6G --cpus 2\n" +
			"  " + brand.Name + " new my-vps --agent claude --seed . --copy ~/.claude/skills\n" +
			"  " + brand.Name + " new my-vps --detach -- htop\n" +
			"  " + brand.Name + " new my-vps --agent claude --detach --json",
		Args: newArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNew(cmd, args, o)
		},
	}
	cmd.Flags().StringVarP(&o.agent, "agent", "a", "shell", "the agent: "+agentKinds())
	cmd.Flags().StringVarP(&o.dir, "dir", "C", "~", "the folder on the host")
	if oldNewShortDir != nil {
		addOldShortFlag(cmd, "dir", "old-dir", "d", &o.usedOldDir, oldNewShortDir)
	}
	cmd.Flags().BoolVar(&o.mkdir, "mkdir", false, "make the folder on the host if it does not exist, without asking")
	addName(cmd, &o.name, "a name for the session (default: the folder name)")
	addDetach(cmd, &o.detach, "start it without attaching")
	cmd.Flags().BoolVarP(&o.worktree, "worktree", "w", false, "give the session its own git worktree and branch")
	cmd.Flags().BoolVar(&o.inline, "inline", false, "start the agent in its inline mode, so your terminal keeps its scrollback (see agents.inline in config.toml)")
	cmd.Flags().StringVar(&o.base, "base", "", "the commit or branch the worktree starts from (default: HEAD)")
	addCPUs(cmd, &o.p114.cpus, 0, "cap the session's CPUs through systemd (default: sandbox.cpus in config.toml)")
	addMemory(cmd, &o.p114.memory, "", "cap the session's memory: auto, or a `size` such as 4G (default: sandbox.memory or sessions.memory_max in config.toml)")
	addMemoryMax(cmd, &o.p114.memoryMax, "cap the session's memory: auto, or a `size` such as 4G (default: sessions.memory_max in config.toml)")
	cmd.Flags().StringArrayVar(&o.p114.envFlags, "env", nil, "set a non-secret environment variable: K=V, or K to read from this laptop (repeatable)")
	cmd.Flags().StringArrayVar(&o.p114.envFiles, "env-file", nil, "read non-secret K=V or K environment variables from a file on this laptop (repeatable)")
	cmd.Flags().StringArrayVar(&o.p114.copyFlags, "copy", nil, "copy a file or folder from this laptop into the session over SFTP: <laptop>[:<guest>] (repeatable)")
	cmd.Flags().StringArrayVar(&o.p114.seedDirs, "seed", nil, "seed the session from a git repository on this laptop as a git bundle (repeatable)")
	cmd.Flags().BoolVar(&o.p114.seedDirty, "seed-dirty", false, "with --seed, include uncommitted changes and non-ignored untracked files as one commit")
	addSecret(cmd, &o.secrets, "pass a secret from config.toml's [secrets] to the session (repeatable, or comma-separated)")
	cmd.Flags().StringSliceVar(&o.expose, "expose", nil, "expose a secret from config.toml's [secrets] directly in a walled session's environment (repeatable, or comma-separated)")
	cmd.Flags().BoolVar(&o.setup, "setup-script", false, "run the repository's setup script this time, whatever your answer for it")
	cmd.Flags().BoolVar(&o.noSetup, "no-setup-script", false, "do not run the repository's setup script this time")
	if oldNewSetup != nil {
		addOldFlag(cmd, "setup-script", "setup", &o.usedOldSetup, oldNewSetup)
	}
	if oldNewNoSetup != nil {
		addOldFlag(cmd, "no-setup-script", "no-setup", &o.usedOldNoSetup, oldNewNoSetup)
	}
	cmd.Flags().StringVar(&o.wallLevel, "wall", "", "sandbox wall level: none, process or gvisor")
	addJSON(cmd, &o.asJSON)
	return cmd
}

// newArgs checks the positional words of new: exactly one host before any "--"
// (cli-spec 10.4, isolation-spec 14.14).
func newArgs(cmd *cobra.Command, args []string) error {
	before := len(args)
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		before = dash
	}
	if before == 0 {
		return missingArgument(cmd, "<host>")
	}
	if before > 1 {
		hint := helpOf(cmd)
		if client.ValidHostName(args[0]) {
			cand := brand.Name + " new " + shellWord(args[0]) + " -- " + joinWords(args[1:])
			if whole(cand) {
				hint = cand
			}
		}
		return &client.UserError{
			Msg:  "new takes one host. A command goes after --",
			Hint: hint,
			Code: "extra_argument",
			Kind: client.KindUsage,
		}
	}
	return nil
}

func runNew(cmd *cobra.Command, args []string, o newOpts) error {
	if o.usedOldDir {
		warnOld(cmd, oldNewShortDir)
	}
	if o.usedOldSetup {
		warnOld(cmd, oldNewSetup)
	}
	if o.usedOldNoSetup {
		warnOld(cmd, oldNewNoSetup)
	}
	if err := beforeAttach(cmd, o.asJSON, o.detach); err != nil {
		return err
	}
	if err := client.CheckSessionName(o.name); err != nil {
		return err
	}
	wl, err := parseWallFlag(o.wallLevel)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("secret") {
		if o.secrets, err = parseNameList("--secret", o.secrets, false, "Pass one or more secret names from [secrets] in config.toml."); err != nil {
			return err
		}
	}
	if cmd.Flags().Changed("expose") {
		if o.expose, err = parseNameList("--expose", o.expose, false, "Pass one or more secret names from [secrets] in config.toml."); err != nil {
			return err
		}
	}
	h, err := client.FindHost(args[0])
	if err != nil {
		return err
	}
	if o.setup && o.noSetup {
		return &client.UserError{Msg: "--setup-script and --no-setup-script cannot go together", Hint: "Pass one of them.", Code: "flags_conflict", Kind: client.KindUsage}
	}
	settings := client.LoadSettings()
	prep, err := prepareNewP114(cmd, h, settings, o.dir, o.worktree, o.p114)
	if err != nil {
		return err
	}
	defer prep.cleanup()
	// Fetch the secrets first: a password manager may want a touch,
	// and a failing command should not cost a connection.
	secretNames := client.GatewaySecretNames(settings, h, o.agent, wl, o.secrets, o.expose)
	values, err := client.FetchSecrets(cmd.Context(), settings, secretNames, secretTerminal(cmd))
	if err != nil {
		return err
	}
	secretBinds := client.SecretBindsFor(settings, secretNames)
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	conn, err := client.Dial(ctx, h, true)
	if err != nil {
		cancel()
		return err
	}
	cols, rows := termSize()
	a := client.TerminalArgs(client.NewArgs{
		Agent: o.agent, Cwd: client.RemotePath(o.dir), MakeFolder: o.mkdir, Name: o.name,
		Worktree: o.worktree, Base: o.base, Inline: o.inline,
		Secrets: values, SecretBinds: secretBinds, ExposedSecrets: o.expose, Wall: wl,
	}, cols, rows)
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		a.Argv = args[dash:]
	}
	if o.setup || o.noSetup {
		a.Setup = setupValue(o.setup)
	}
	cancel()
	s, err := startNewWithP114(cmd, conn, h, a, prep)
	if err != nil {
		conn.Close()
		return err
	}
	// An address without a host finds the new session at once.
	client.RememberSession(h.Name, *s)
	conn.Close()
	if !o.detach {
		return attach(cmd.Context(), h, s.ID, s.Name)
	}
	if o.asJSON {
		return printJSON(sessionObject(h.Name, *s))
	}
	say(cmd, "Started %s/%s (%s, %s).\n", shown(h.Name), shown(s.ID), shown(s.Name), shown(s.Agent))
	return nil
}

func parseWallFlag(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	w := strings.ToLower(strings.TrimSpace(raw))
	switch w {
	case "none", "process", "gvisor", "plain":
		return w, nil
	default:
		return "", &client.UserError{
			Msg:  fmt.Sprintf("--wall: unknown wall level %q", raw),
			Hint: "Pass none, process or gvisor.",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
}

// listSessions asks a host for its sessions. Tests replace it.
var listSessions = func(ctx context.Context, conn *client.Conn) ([]client.Session, error) { return conn.Sessions(ctx) }

func renameCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rename <host>/<session> <name>",
		Short:   "Rename a session",
		Example: "  " + brand.Name + " rename my-vps/auth-fix login-fix",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := strings.TrimSpace(args[1])
			if want == "" {
				return &client.UserError{Msg: "a name is needed", Hint: "Pass a non-empty name for the session.", Code: "bad_name", Kind: client.KindUsage}
			}
			if err := client.CheckSessionName(want); err != nil {
				return err
			}
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				if err := conn.RenameSession(ctx, s.ID, want); err != nil {
					return err
				}
				updated := *s
				updated.Name = want
				client.RememberSession(h.Name, updated)
				say(cmd, "Renamed %s to %s on %s.\n", shown(nameOf(*s)), shown(want), shown(h.Name))
				return nil
			})
		},
	}
}

type hostSessions struct {
	Host     string           `json:"host"`
	Sessions []client.Session `json:"sessions"`
	Error    string           `json:"error,omitempty"`
	// err is the error behind Error, and at is when the host's list arrived.
	err error
	at  time.Time
}

// rememberLists writes the lists of the hosts that answered to the session
// cache, each with the time when it arrived, so that the picker, the
// launcher and bare session addresses read them at once.
func rememberLists(results []hostSessions) {
	lists := map[string]client.HostList{}
	for _, r := range results {
		if r.err == nil {
			at := r.at
			if at.IsZero() {
				at = time.Now()
			}
			lists[r.Host] = client.HostList{Sessions: r.Sessions, At: at}
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
			if err != nil {
				results[i].Error, results[i].err = err.Error(), err
			} else {
				results[i].at = time.Now()
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
			extra := ""
			if s.CopiesPending {
				extra = "  (copies not sent)"
			}
			fmt.Printf("%-14s %-10s %-20s %-9s %-*s %6s  %s%s\n", host, shown(s.ID), trunc(shown(s.Name), 20), shown(s.Agent), stateW, stateLabel(s), age(s.CreatedAt), shown(s.Cwd), extra)
		}
	}
}

func lsCmd() *cobra.Command {
	var hostName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls [<host>]",
		Short:   "List sessions on every host, or on one",
		Example: "  " + brand.Name + " ls\n  " + brand.Name + " ls my-vps --json",
		Args:    cobra.MaximumNArgs(1),
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
		Use:     "send <host>/<session> <text>",
		Short:   "Type text into a session without attaching, then press Enter",
		Example: "  " + brand.Name + " send my-vps/auth-fix \"run the tests and fix what fails\"",
		Args:    cobra.ExactArgs(2),
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
