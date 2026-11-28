package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/wall"
)

// agents login (docs/contributing/cli-spec.md 12.3): the agent's own login,
// in a session on the host. The client never reads, stores or passes the
// agent's credentials. The agent writes them on the host. What the host
// sends is printed cleaned, and a session's address always uses its ID,
// which the client checks first: two logins that start at the same moment
// share a name.

func agentsLoginCmd() *cobra.Command {
	var hostName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "login <agent> [<host>]",
		Short: "Start an agent's own login on a host, in a session you can answer",
		Long: "Starts the agent's own login, such as `claude auth login`, in a session named\n" +
			"`login-<agent>` on the host, and attaches to it. When that login already runs, it\n" +
			"attaches to it instead. When you leave, it checks the login again. The login runs\n" +
			"again also when the agent is logged in, for example to change the account. The\n" +
			"agent keeps its login on the host. " + brand.DisplayName + " never sees it.",
		Example: "  " + brand.Name + " agents login claude my-vps\n" +
			"  " + brand.Name + " agents login claude my-vps --json   # start it, and print the session",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := loginAgent(args[0], hostWord(args, hostName))
			if err != nil {
				return err
			}
			h, err := agentHost(cmd, args, hostName)
			if err != nil {
				return err
			}
			if asJSON {
				return loginJSON(cmd, h, a)
			}
			return logIn(cmd.Context(), h, a, inputOn(cmd))
		},
	}
	cmd.Flags().StringVar(&hostName, "host", "", "the host, as the second argument names it")
	addJSON(cmd, &asJSON)
	return cmd
}

// hostWord is the host as the user typed it: the second argument, or
// --host. It is "" when the user named none.
func hostWord(args []string, flag string) string {
	if len(args) > 1 {
		return args[1]
	}
	return flag
}

// loginAgent is the agent of kind, for agents login. An unknown agent fails
// with exit code 3, and an agent without a login of its own with exit code
// 2, with the variables that log it in. host is the host that the user
// named, if any, for the Try: line.
func loginAgent(kind, host string) (agents.Agent, error) {
	a, err := findAgent(kind, loginKinds())
	if err != nil || a.Setup != nil && a.Setup.Login != "" {
		return a, err
	}
	if a.Setup == nil || len(a.Setup.LoginEnv) == 0 {
		hint := brand.Name + " new <host> --agent " + a.Kind
		switch {
		case client.ValidHostName(host):
			hint = brand.Name + " new " + shellWord(host) + " --agent " + a.Kind
		case host != "":
			hint = brand.Name + " host ls"
		}
		return a, &client.UserError{
			Msg:  a.Kind + " has no login of its own, and needs none",
			Hint: hint,
			Code: "no_login",
			Kind: client.KindUsage,
		}
	}
	return a, &client.UserError{
		Msg:  fmt.Sprintf("%s has no login of its own. It reads %s", a.Kind, orList(a.Setup.LoginEnv)),
		Hint: "Add the key under [secrets] in config.toml, and pass it with --secret.",
		Code: "no_login",
		Kind: client.KindUsage,
	}
}

// loginKinds lists the agents that agents login knows about: those with a
// login, or the variables that log them in.
func loginKinds() []string {
	var out []string
	for _, a := range agents.Table {
		if a.Setup != nil && (a.Setup.Login != "" || len(a.Setup.LoginEnv) > 0) {
			out = append(out, a.Kind)
		}
	}
	return out
}

// orList joins words as a sentence does: "A", "A or B", "A, B or C".
func orList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	last := len(words) - 1
	return strings.Join(words[:last], ", ") + " or " + words[last]
}

// loginAttach attaches to the session of a login, and reports whether the
// session ended and whether the terminal reader stayed stuck. Tests replace it.
var loginAttach = attachSession

// logIn runs a's own login on h: it finds the login that runs, or starts
// one. With attachOK, which input on gives, it attaches, then checks the
// login again. Otherwise it says how to attach.
func logIn(ctx context.Context, h client.Host, a agents.Agent, attachOK bool) error {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	conn, err := client.Dial(cctx, h, attachOK)
	if err != nil {
		cancel()
		return err
	}
	s, running, err := loginSession(cctx, conn, h, a)
	conn.Close()
	cancel()
	if err != nil {
		return err
	}
	target := client.CommandTarget(h.Name, s.ID)
	printWalledLoginNote(h)
	switch {
	case !attachOK && running:
		fmt.Fprintf(os.Stderr, "The login of %s already runs in %s. Attach to finish the login: %s attach %s\n", a.Title, target, brand.Name, target)
		return nil
	case !attachOK:
		fmt.Fprintf(os.Stderr, "Started %s. Attach to finish the login: %s attach %s\n", target, brand.Name, target)
		return nil
	}
	// Both kinds of attach name the detach key of config.toml.
	leave := fmt.Sprintf("Answer it there, or press %s to leave it running.", client.CleanLine(client.LoadSettings().Attach.DetachKey))
	if running {
		fmt.Fprintf(os.Stderr, "The login of %s already runs in %s. Attaching to it. %s\n", a.Title, target, leave)
	} else {
		args, _ := onboard.LoginArgs(a)
		fmt.Fprintf(os.Stderr, "%s logs in with its own login, in a session on %s:\n\n    %s\n\n", a.Title, h.Name, a.Setup.Login)
		fmt.Fprintf(os.Stderr, "It runs in the session %s (%s). %s\n", args.Name, target, leave)
	}
	ended, stuck, err := loginAttach(ctx, h, s.ID, s.Name)
	if err != nil {
		return err
	}
	return afterLogin(ctx, h, a, s.ID, ended, !stuck)
}

// printWalledLoginNote reminds the user when h's sandbox floor is above none
// that walled sessions never see host login files (cli-spec 12.3, rule 10).
func printWalledLoginNote(h client.Host) {
	if h.WallFloor == "" {
		return
	}
	lvl, err := wall.ParseLevel(h.WallFloor)
	if err != nil || lvl <= wall.None {
		return
	}
	fmt.Fprintf(os.Stderr, "Note: %s has sandbox floor %s. Walled sessions never see host login files; add a token under [secrets] in config.toml (such as CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`) and pass it with --secret.\n", h.Name, lvl)
}

// loginJSON finds the login of a that runs on h, or starts one without
// attaching, and prints its session object (cli-spec 7.1), with started:
// true when the command started it. Its address is always host/ID, since
// two logins share the name (cli-spec 12.3, rule 11). printJSON physically
// escapes unsafe runes while preserving decoded keys and values (cli-spec 7.4).
func loginJSON(cmd *cobra.Command, h client.Host, a agents.Agent) error {
	cctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
	defer cancel()
	conn, err := client.Dial(cctx, h, false)
	if err != nil {
		return err
	}
	s, running, err := loginSession(cctx, conn, h, a)
	conn.Close()
	if err != nil {
		return err
	}
	obj := sessionObject(h.Name, s)
	obj["address"] = h.Name + "/" + s.ID
	obj["started"] = !running
	return printJSON(obj)
}

// loginSession checks a on the host of conn, then finds the login of a that
// runs, the newer of two, or starts one in a session named login-<agent>,
// with no secret. It reports whether the login ran before. The session's
// ID is checked, so that it is safe to print and to put in a command.
func loginSession(ctx context.Context, conn *client.Conn, h client.Host, a agents.Agent) (client.Session, bool, error) {
	checks, err := conn.AgentChecks(ctx)
	if err != nil {
		return client.Session{}, false, err
	}
	if err := loginReady(h, a, checks); err != nil {
		return client.Session{}, false, err
	}
	list, err := conn.Sessions(ctx)
	if err != nil {
		return client.Session{}, false, err
	}
	args, _ := onboard.LoginArgs(a)
	s, running := runningLogin(list, args.Name)
	if !running {
		cols, rows := termSize()
		started, err := conn.NewSession(ctx, client.TerminalArgs(args, cols, rows))
		if err != nil {
			return client.Session{}, false, err
		}
		s = *started
	}
	if !api.ValidSessionID(s.ID) {
		return client.Session{}, false, &client.UserError{
			Msg:  fmt.Sprintf("%s answered with a session ID that is not one: %+q", h.Name, client.CleanLine(s.ID)),
			Hint: brand.Name + " host add " + shellWord(h.Name) + " (reinstalls the helper)",
			Code: "protocol",
		}
	}
	return s, running, nil
}

// loginReady fails when the host cannot run a's login: the agent is not
// installed, or a login shell does not find it.
func loginReady(h client.Host, a agents.Agent, checks []api.AgentCheck) error {
	c, ok := findCheck(checks, a.Kind)
	switch {
	case !ok || !c.Installed:
		return &client.UserError{
			Msg:  fmt.Sprintf("%s is not installed on %s", a.Title, h.Name),
			Hint: onboard.InstallCommand(h.Name, a.Kind),
			Code: "agent_missing",
		}
	case !c.OnPath:
		c = cleanCheck(a, c)
		return &client.UserError{
			Msg:  fmt.Sprintf("%s is on %s, at %s, but a login shell does not find it", a.Title, h.Name, c.Path),
			Hint: onboard.Describe(h.Name, c).Try,
			Code: "agent_not_on_path",
		}
	}
	return nil
}

// cleanCheck is c as the client prints it: the agent table's title, and
// the host's path and reason without control or hidden characters.
func cleanCheck(a agents.Agent, c api.AgentCheck) api.AgentCheck {
	c.Kind, c.Title = a.Kind, a.Title
	c.Path, c.LoginHow = client.CleanPath(c.Path), client.CleanLine(c.LoginHow)
	return c
}

// runningLogin is the newest session named name in list that has not ended.
func runningLogin(list []client.Session, name string) (client.Session, bool) {
	var found client.Session
	ok := false
	for _, s := range list {
		if s.Name == name && !client.Ended(s) && (!ok || !s.CreatedAt.Before(found.CreatedAt)) {
			found, ok = s, true
		}
	}
	return found, ok
}

// afterLogin checks the login again, as host doctor does, and prints the
// agent's line. A login that still runs gets the line that says how to
// attach to it again, in place of the line's own next step. After a detach
// the attach is done (cli-spec 5.1), so a check that fails then says so,
// and the command still ends with 0.
func afterLogin(ctx context.Context, h client.Host, a agents.Agent, id string, ended, interactive bool) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	target := client.CommandTarget(h.Name, id)
	again := fmt.Sprintf("\nThe login keeps running. Attach again: %s attach %s\n", brand.Name, target)
	c, running, err := loginState(cctx, h, a, id, ended, interactive)
	if err != nil && ended {
		return err
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nThe check of the login failed: %s.\n%s", strings.TrimSuffix(client.CleanLine(err.Error()), "."), again)
		return nil
	}
	l := onboard.Describe(h.Name, c)
	if running {
		l.Try = ""
	}
	fmt.Fprintln(os.Stderr)
	printLine(os.Stderr, l)
	if running {
		fmt.Fprint(os.Stderr, again)
	}
	return nil
}

// loginState checks a on h again, as host doctor does. After a detach it
// also asks whether the session id still runs, since a login can end while
// the client checks it.
func loginState(ctx context.Context, h client.Host, a agents.Agent, id string, ended, interactive bool) (api.AgentCheck, bool, error) {
	conn, err := client.Dial(ctx, h, interactive)
	if err != nil {
		return api.AgentCheck{}, false, err
	}
	defer conn.Close()
	checks, err := conn.AgentChecks(ctx)
	if err != nil {
		return api.AgentCheck{}, false, err
	}
	c, _ := findCheck(checks, a.Kind)
	c = cleanCheck(a, c)
	if ended {
		return c, false, nil
	}
	list, err := conn.Sessions(ctx)
	if err != nil {
		return c, false, err
	}
	for _, s := range list {
		if s.ID == id {
			return c, !client.Ended(s), nil
		}
	}
	return c, false, nil
}
