package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// rm deletes a session (docs/contributing/cli-spec.md 9.5). It asks first
// when input is on, with the lines of the launcher's delete sheet. kill is
// its old name, which never asks.

func rmCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm [<host>/<session>|-]",
		Aliases: []string{"delete"},
		Short:   "Delete a session, after a question",
		Long: "Stops the session's agent and removes the session from the host. The session's folder stays. A\n" +
			"worktree that the session made goes with its branch, unless it has changes or new commits. At a\n" +
			"terminal, rm asks first, and without a session it lets you pick one from a list. The host may be left\n" +
			"out when no other host has a session of that name.",
		Example: "  " + brand.Name + " rm my-vps/auth-fix\n" +
			"  " + brand.Name + " rm my-vps/auth-fix -y   # no question, as in a script\n" +
			"  " + brand.Name + " rm auth-fix             # the host is found when the name is unique\n" +
			"  " + brand.Name + " rm                      # pick one from a list",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			// An address without a host must be sure: a host that did not
			// answer may have a session of the same name (cli-spec 9.2).
			a, err := sessionArg(cmd, args, true)
			if err != nil {
				return err
			}
			return removeSession(cmd, a, yes)
		},
	}
	addYes(cmd, &yes, "delete the session")
	return cmd
}

// killCmd is the old name of rm -y. It never asks, as before, and warns,
// also without a session: then it fails with the usage of rm.
func killCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "kill <host>/<session>",
		Short:   `Delete a session without asking: the old name of "rm -y"`,
		Example: "  " + brand.Name + " rm my-vps/auth-fix -y",
		Hidden:  true,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnOld(cmd, oldKill)
			if len(args) == 0 {
				rm, _, err := cmd.Root().Find([]string{"rm"})
				if err != nil {
					return fmt.Errorf("find rm: %w", err)
				}
				return missingArgument(rm, "a session, as <host>/<session>")
			}
			a, err := resolveAddress(cmd, args[0], true)
			if err != nil {
				return err
			}
			return removeSession(cmd, a, true)
		},
	}
}

// removeSession deletes the session that a names, after the question
// unless yes, and takes it out of the session cache.
func removeSession(cmd *cobra.Command, a address, yes bool) error {
	return a.open(cmd.Context(), cmd, func(ctx context.Context, f found) error {
		asked := false
		if !yes {
			var err error
			if asked, err = confirmDelete(cmd, sessionPrompts(cmd, f.host.Name, f.s.ID), f); err != nil {
				return err
			}
		}
		// After the question, the rest gets its own time.
		dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if asked {
			afterQuestion(f)
			if err := stillTheSame(dctx, f); err != nil {
				return err
			}
		}
		// The host deletes only while the name is the one confirmed.
		beforeDelete(f)
		res, err := f.conn.DeleteNamed(dctx, f.s.ID, f.s.Name)
		if err != nil {
			return err
		}
		client.ForgetSession(f.host.Name, f.s.ID)
		say(cmd, "Deleted %s (%s) on %s.\n", shown(f.s.Name), shown(f.s.ID), shown(f.host.Name))
		note, hint := client.WorktreeNote(f.host.Name, res)
		if note != "" {
			say(cmd, "%s\n", sentence(note))
		}
		if hint != "" {
			say(cmd, "Try: %s\n", hint)
		}
		return nil
	})
}

// confirmDelete asks before rm deletes the session f found, and reports
// whether it asked. With input off, rm deletes and warns until the end of
// its old form, then needs -y (question C-10). The answer no cancels.
func confirmDelete(cmd *cobra.Command, p *prompter, f found) (bool, error) {
	if !p.on && oldRmWithoutYes != nil {
		warnOld(cmd, oldRmWithoutYes)
		return false, nil
	}
	ok, err := askDelete(p, f.host.Name, *f.s)
	if err != nil {
		return true, err
	}
	if !ok {
		return true, cancelled()
	}
	return true, nil
}

// afterQuestion runs after the answer yes, before rm looks again. Tests
// set it to rename the session while the question waits.
var afterQuestion = func(found) {}

// beforeDelete runs just before rm's delete request. Tests set it to
// rename the session after rm's last look, which the host must catch.
var beforeDelete = func(found) {}

// stillTheSame checks, after the answer, that the session f found is still
// the one that the user confirmed: on the host's live list, with the same
// ID and the same name. A session that is gone, or that a rename changed
// while the question waited, is not deleted.
func stillTheSame(ctx context.Context, f found) error {
	list, err := listSessions(ctx, f.conn)
	if err != nil {
		return err
	}
	name, hostName := shown(nameOf(*f.s)), shown(f.host.Name)
	now := byID(list, f.s.ID)
	switch {
	case now == nil:
		return &client.UserError{Msg: fmt.Sprintf("%s on %s went while the question waited", name, hostName),
			Hint: lsOf(f.host.Name), Code: "session_not_found", Kind: client.KindNotFound}
	case now.Name != f.s.Name:
		return &client.UserError{Msg: fmt.Sprintf("%s on %s was renamed %s while the question waited, so it was not deleted", name, hostName, shown(nameOf(*now))),
			Hint: lsOf(f.host.Name), Code: "session_renamed"}
	}
	return nil
}

// askDelete asks whether to delete the session s of host, with what stops
// and what stays. The question names the session by its name. The Try:
// line of p, when input is off, names it by host/ID (sessionPrompts).
func askDelete(p *prompter, host string, s client.Session) (bool, error) {
	where := shown(nameOf(s)) + " on " + shown(host)
	lines := make([]string, 0, 4)
	lines = append(lines, "Delete "+where+"?")
	lines = append(lines, client.DeleteLines(s, host, s.Cwd)...)
	lines = append(lines, "Delete it?")
	return p.confirm(qDelete, strings.Join(lines, "\n"), "a yes to delete "+where)
}

// nameOf is a session's name, or its ID when it has none.
func nameOf(s client.Session) string {
	if s.Name == "" {
		return s.ID
	}
	return s.Name
}
