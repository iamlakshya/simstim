package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

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
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return false, false, &client.UserError{Msg: "attach needs a terminal", Hint: "Run it from a terminal, not a pipe.", Code: "no_terminal"}
	}
	tui.RememberLast(h.Name, id, name)
	name = client.CleanLine(name) // only printed after the last-session record
	res, err := client.Attach(ctx, client.AttachOptions{
		Host: h, Session: id, Settings: client.LoadSettings(), Interactive: true,
	})
	if err != nil {
		return false, res.ReaderStuck, err
	}
	if res.Ended != nil {
		fmt.Fprint(os.Stderr, endMessage(name, h.Name, id, res.Ended))
	} else {
		fmt.Fprintf(os.Stderr, "\r\nDetached. %s keeps running on %s.\n", name, h.Name)
	}
	return res.Ended != nil, res.ReaderStuck, nil
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "attach [<host>/<session> | -]",
		Aliases: []string{"a"},
		Short:   "Attach to a session (detach with Ctrl+\\); - is the last one, and without one you pick from a list",
		Example: "  " + brand.Name + " attach my-vps/auth-fix\n" +
			"  " + brand.Name + " attach -   # the last session you attached to\n" +
			"  " + brand.Name + " attach     # pick one from a list",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var target string
			if len(args) == 1 {
				target = args[0]
			} else {
				t, err := pickSession(cmd, "<host>/<session>")
				if err != nil {
					return err
				}
				target = t
			}
			if target == "-" {
				host, id, ok := tui.LastSession()
				if !ok {
					return &client.UserError{Msg: "no session has been attached yet", Hint: "Attach to a session by its name, or run " + brand.Name + " and pick one.", Code: "no_last_session", Kind: client.KindNotFound}
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

// pickSession opens the picker for a command whose address is missing, and
// returns the address picked (cli-spec 6.3). With input off, the missing
// address is the usage error of a missing argument. want names the address
// in that error.
func pickSession(cmd *cobra.Command, want string) (string, error) {
	if !inputOn(cmd) {
		return "", missingArgument(cmd, "a session, as "+want)
	}
	hosts, err := client.LoadHosts()
	if err != nil {
		return "", err
	}
	if len(hosts) == 0 {
		return "", &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
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
		return "", cancelled()
	case errors.Is(err, tui.ErrNoSessions):
		return "", &client.UserError{Msg: "no sessions on any host", Hint: brand.Name + " new " + hosts[0].Name, Code: "no_sessions", Kind: client.KindNotFound}
	case err != nil:
		return "", err
	}
	return picked.Host + "/" + picked.Session.ID, nil
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

func flipCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "flip [<host>/<session>]",
		Short: "Switch sessions quickly: the session list of every host, then attach",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return attachCmd().RunE(cmd, args)
			}
			if err := checkLauncher(cmd, "the switcher", brand.Name+" flip <host>/<session>, or "+brand.Name+" ls"); err != nil {
				return err
			}
			return tui.RunWith(cmd.Context(), tui.Options{Switcher: true, Local: onboard.System{Providers: providers}})
		},
	}
}

// sentence is s with a capital letter first and a full stop at its end.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}
