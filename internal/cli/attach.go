package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/tui"
)

// attach runs a session in this terminal.
func attach(ctx context.Context, h client.Host, id, name string) error {
	_, err := attachEnded(ctx, h, id, name)
	return err
}

// attachEnded runs a session in this terminal, and reports whether the
// session ended (rather than the user detaching). What it says after the
// attach goes to stderr: the session's own bytes are the only ones on
// stdout.
func attachEnded(ctx context.Context, h client.Host, id, name string) (bool, error) {
	ended, _, err := attachSession(ctx, h, id, name)
	return ended, err
}

func attachSession(ctx context.Context, h client.Host, id, name string) (ended, stuck bool, err error) {
	res, err := attachResult(ctx, h, id, name)
	return res.Ended != nil, res.ReaderStuck, err
}

func attachResult(ctx context.Context, h client.Host, id, name string) (client.AttachResult, error) {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return client.AttachResult{}, &client.UserError{Msg: "attach needs a terminal", Hint: "Run it from a terminal, not a pipe.", Code: "no_terminal", Kind: client.KindUsage}
	}
	client.RememberLast(h.Name, id, name)
	// The name comes from the host: it is cleaned before it is drawn. The
	// session's own bytes pass as they are.
	name = shown(name)
	res, err := client.Attach(ctx, client.AttachOptions{
		Host: h, Session: id, Settings: client.LoadSettings(), Interactive: true,
	})
	if err != nil {
		return res, err
	}
	if res.Ended != nil {
		fmt.Fprint(os.Stderr, endMessage(name, h.Name, id, res.Ended))
	} else {
		fmt.Fprintf(os.Stderr, "\r\nDetached. %s keeps running on %s.\n", name, shown(h.Name))
	}
	return res, nil
}

func attachCmd() *cobra.Command {
	var restart bool
	var cpus int
	var memory string
	cmd := &cobra.Command{
		Use:     "attach [<host>/<session>|-]",
		Aliases: []string{"a"},
		Short:   "Attach to a session",
		Long: "Attaches this terminal to a session. A stopped host that its provider script can start wakes first,\n" +
			"and a stopped session wakes on attach (with --cpus and --memory when given). When the session's\n" +
			"agent has ended, attach asks to start it again, as resume does. --restart answers yes. The host may\n" +
			"be left out when no other host has a session of that name.",
		Example: "  " + brand.Name + " attach my-vps/auth-fix\n" +
			"  " + brand.Name + " attach -          # the last session you attached to\n" +
			"  " + brand.Name + " attach auth-fix   # the host is found when the name is unique\n" +
			"  " + brand.Name + " attach            # pick one from a list",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttachOpts(cmd, args, restart, cpus, memory)
		},
	}
	addRestart(cmd, &restart)
	addCPUs(cmd, &cpus, 0, "when waking a stopped session, cap its CPUs through systemd")
	addMemory(cmd, &memory, "", "when waking a stopped session, cap its memory: auto, or a `size` such as 4G")
	return cmd
}

// addRestart gives cmd the flag that answers attach's question about an
// ended agent.
func addRestart(cmd *cobra.Command, p *bool) {
	cmd.Flags().BoolVar(p, "restart", false, "when the agent has ended, start it again without asking")
}

// runAttach attaches to the session that args name, or that the user
// picks (cli-spec 9.3). It needs a terminal. Without a session, the picker
// or its missing argument comes first. With one, the check for a terminal
// comes before any lookup, so that a script opens no connection to a host,
// and no host wakes for nothing. On an ended agent it asks to start the
// agent again, and then does what resume does.
func runAttach(cmd *cobra.Command, args []string, restart bool) error {
	return runAttachOpts(cmd, args, restart, 0, "")
}

func runAttachOpts(cmd *cobra.Command, args []string, restart bool, cpus int, memory string) error {
	wakeArgs, err := parseAttachWakeArgs(cmd, cpus, memory)
	if err != nil {
		return err
	}
	if len(args) > 0 && (!isTerminal(os.Stdin) || !isTerminal(os.Stdout)) {
		// No lookup comes first, so the hint repeats the address only when
		// it means one session as it is.
		return &client.UserError{Msg: "attach needs a terminal", Hint: brand.Name + " send " + typedAddress(args[0]) + " 'text'",
			Code: "no_terminal", Kind: client.KindUsage}
	}
	a, err := sessionArg(cmd, args, false)
	if err != nil {
		return err
	}
	var s client.Session
	err = a.open(cmd.Context(), cmd, func(ctx context.Context, f found) error {
		s = *f.s
		if s.State == api.StateStopped {
			r, err := restartAgent(ctx, cmd, f, true, wakeArgs)
			if err == nil {
				s = *r
			}
			return err
		}
		if !client.Ended(s) {
			if s.CopiesPending {
				if err := resendPendingCopies(ctx, f.conn, f.host, &s); err != nil {
					return err
				}
			}
			if restart {
				say(cmd, "The agent of %s on %s runs, so attach did not restart it.\n", shown(nameOf(s)), shown(f.host.Name))
			}
			if wakeArgs.MemoryMax != 0 || wakeArgs.CPUs != 0 {
				say(cmd, "%s on %s is already running; its size was not changed. Pass the size flags again at the next wake.\n", shown(nameOf(s)), shown(f.host.Name))
			}
			return nil
		}
		r, err := restartAgent(ctx, cmd, f, restart, wakeArgs)
		if err == nil {
			s = *r
		}
		return err
	})
	if err != nil {
		return err
	}
	return attach(cmd.Context(), a.host, s.ID, s.Name)
}

func parseAttachWakeArgs(cmd *cobra.Command, cpus int, memory string) (client.ResumeArgs, error) {
	var wakeArgs client.ResumeArgs
	if cmd.Flags().Changed("memory") {
		n, err := client.ParseMemoryFlagNamed("--memory", memory)
		if err != nil {
			return wakeArgs, err
		}
		wakeArgs.MemoryMax = n
	}
	cpusFlag, hasCPUs, err := parseCPUsFlag(cmd, cpus)
	if err != nil {
		return wakeArgs, err
	}
	if hasCPUs {
		wakeArgs.CPUs = cpusFlag
	}
	return wakeArgs, nil
}

// restartAgent starts again the ended agent of the session f found, as
// resume does, after a yes or with restart. With input off and without
// restart, the question fails with exit code 4. The answer no says why the
// agent ended, and cancels. A helper that cannot resume gets the error of
// an ended agent.
func restartAgent(ctx context.Context, cmd *cobra.Command, f found, restart bool, wakeArgs client.ResumeArgs) (*client.Session, error) {
	if !f.conn.HasFeature("resume") {
		return nil, endedError(f.host, *f.s)
	}
	if !restart {
		ok, err := askRestart(sessionPrompts(cmd, f.host.Name, f.s.ID), f.host.Name, *f.s)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, declined(f.host, *f.s)
		}
	}
	res, now, err := resumeRacing(ctx, f, wakeArgs, nil, secretTerminal(cmd))
	if now != nil {
		say(cmd, "Another client started the agent of %s on %s again first, so this attaches.\n", shown(nameOf(*now)), shown(f.host.Name))
		return now, nil
	}
	if err != nil {
		return nil, err
	}
	debugStopped(cmd, f.host, res)
	return &res.Session, nil
}

// askRestart asks whether to start the ended agent of the session s of
// host again. The question names the session by its name. The Try: line
// of p, when input is off, names it by host/ID (sessionPrompts).
func askRestart(p *prompter, host string, s client.Session) (bool, error) {
	where, agent := shown(nameOf(s))+" on "+shown(host), shown(s.Agent)
	return p.confirm(qRestart, fmt.Sprintf("Start %s again in %s?", agent, where), fmt.Sprintf("a yes to start %s again in %s", agent, where))
}

// declined is the answer no at the question of an ended agent: it says why
// the agent ended, as the error of an ended agent does, and cancels.
func declined(h client.Host, s client.Session) error {
	var ue *client.UserError
	if !errors.As(endedError(h, s), &ue) {
		return cancelled()
	}
	return &client.UserError{Msg: ue.Msg, Hint: ue.Hint, Code: "cancelled", Kind: client.KindCancelled}
}

// pickSession opens the picker for a command whose address is missing, and
// returns the session picked, by its ID, which the host's list then finds
// (cli-spec 6.3). With input off, the missing address is the usage error
// of a missing argument. want names the address in that error.
func pickSession(cmd *cobra.Command, want string) (address, error) {
	if !inputOn(cmd) {
		return address{}, missingArgument(cmd, "a session, as "+want)
	}
	hosts, err := client.LoadHosts()
	if err != nil {
		return address{}, err
	}
	if len(hosts) == 0 {
		return address{}, &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	rows := tui.CachedSessions(names)
	picked, err := tui.Pick(cmd.Context(), tui.PickOptions{
		Title: shortPath(cmd), Hosts: names, Rows: rows, Refresh: pickRefresh(hosts, rows), In: os.Stdin, Out: os.Stderr,
	})
	switch {
	case errors.Is(err, tui.ErrPickCancelled):
		return address{}, cancelled()
	case errors.Is(err, tui.ErrNoSessions):
		return address{}, &client.UserError{Msg: "no sessions on any host", Hint: newHostHint(hosts[0].Name), Code: "no_sessions", Kind: client.KindNotFound}
	case err != nil:
		return address{}, err
	}
	return pickedAddress(picked)
}

// pickedAddress is the address of a picked row: its session's ID, which
// the host's list finds by its ID alone.
func pickedAddress(picked tui.PickRow) (address, error) {
	h, err := client.FindHost(picked.Host)
	if err != nil {
		return address{}, err
	}
	return address{host: h, ref: picked.Session.ID, byID: true, gone: fmt.Sprintf("%s on %s is gone", shown(nameOf(picked.Session)), shown(h.Name))}, nil
}

// pickRefresh asks every host for its sessions, as ls does, without waking
// a stopped host, and writes the lists to the session cache. A host that
// does not answer keeps its rows of cached, whatever their age
// (cli-spec 9.2). When no host answers, the error is the first host's.
func pickRefresh(hosts []client.Host, cached []tui.PickRow) func(context.Context) ([]tui.PickRow, error) {
	return func(ctx context.Context) ([]tui.PickRow, error) {
		results := listAll(ctx, hosts)
		rememberLists(results)
		var rows []tui.PickRow
		var unreached error
		answered := false
		for _, r := range results {
			if r.err != nil {
				if unreached == nil {
					unreached = r.err
				}
				for _, c := range cached {
					if c.Host == r.Host {
						rows = append(rows, c)
					}
				}
				continue
			}
			answered = true
			for _, s := range r.Sessions {
				rows = append(rows, tui.PickRow{Host: r.Host, Session: s})
			}
		}
		if answered {
			return rows, nil
		}
		return rows, unreached
	}
}

// flipCmd is the old name of attach. Alone, it opens the launcher's
// switcher, as before. Both warn.
func flipCmd() *cobra.Command {
	var restart bool
	cmd := &cobra.Command{
		Use:     "flip [<host>/<session>|-]",
		Short:   `Switch sessions: the old name of "attach"`,
		Example: "  " + brand.Name + " flip my-vps/auth-fix",
		Hidden:  true,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnOld(cmd, oldFlip)
			if len(args) == 1 {
				return runAttach(cmd, args, restart)
			}
			if err := checkLauncher(cmd, "the switcher", brand.Name+" flip <host>/<session>, or "+brand.Name+" ls"); err != nil {
				return err
			}
			return tui.RunWith(cmd.Context(), tui.Options{Switcher: true, Local: onboard.System{Providers: providers}})
		},
	}
	addRestart(cmd, &restart)
	return cmd
}

// sentence is s with a capital letter first and a full stop at its end.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}
