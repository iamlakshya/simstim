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

// shell opens a shell session inside a session's sandbox (or on the host for
// unwalled sessions) (isolation spec 6.11).
func shellCmd() *cobra.Command {
	var createNew, detach, asJSON bool
	var name string
	cmd := &cobra.Command{
		Use:   "shell [<host>/<session>|-]",
		Short: "Open a shell inside a session's sandbox",
		Long: "Opens an interactive shell in the same sandbox as a session, sharing its files and\n" +
			"network policy. When a shell is already running in that sandbox, it attaches to the\n" +
			"newest one unless --new is set. On an unwalled session, it opens a host shell in\n" +
			"the same folder.",
		Example: "  " + brand.Name + " shell my-vps/auth-fix\n" +
			"  " + brand.Name + " shell my-vps/auth-fix --new --name tests\n" +
			"  " + brand.Name + " shell my-vps/auth-fix --detach --json",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := beforeAttach(cmd, asJSON, detach); err != nil {
				return err
			}
			if err := client.CheckSessionName(name); err != nil {
				return err
			}
			a, err := sessionArg(cmd, args, false)
			if err != nil {
				return err
			}
			return runShell(cmd, a, createNew, detach, asJSON, name)
		},
	}
	cmd.Flags().BoolVar(&createNew, "new", false, "start a new shell even when one is already running in the sandbox")
	addName(cmd, &name, "a name for the new shell session")
	addDetach(cmd, &detach, "start or pick the shell without attaching")
	addJSON(cmd, &asJSON)
	return cmd
}

func runShell(cmd *cobra.Command, a address, createNew, detach, asJSON bool, name string) error {
	var targetHost client.Host
	var sess *client.Session
	var created bool
	err := a.open(cmd.Context(), cmd, func(ctx context.Context, f found) error {
		sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cols, rows := termSize()
		s, started, err := client.OpenShellSession(sctx, f.conn, *f.s, client.ShellOptions{
			New:      createNew,
			Name:     name,
			Cols:     cols,
			Rows:     rows,
			Settings: client.LoadSettings(),
		})
		if err != nil {
			return err
		}
		client.RememberSession(f.host.Name, *s)
		targetHost, sess, created = f.host, s, started
		return nil
	})
	if err != nil {
		return err
	}
	if !detach {
		return attach(cmd.Context(), targetHost, sess.ID, sess.Name)
	}
	if asJSON {
		obj := sessionObject(targetHost.Name, *sess)
		obj["started"] = created
		return printJSON(obj)
	}
	if created {
		say(cmd, "Started %s/%s (%s, %s).\n", shown(targetHost.Name), shown(sess.ID), shown(sess.Name), shown(sess.Agent))
	} else {
		say(cmd, "Using %s/%s (%s).\n", shown(targetHost.Name), shown(sess.ID), shown(sess.Name))
	}
	return nil
}

func warnOldNewFlags(cmd *cobra.Command, o newOpts) {
	if o.usedOldDir {
		warnOld(cmd, oldNewShortDir)
	}
	if o.usedOldSetup {
		warnOld(cmd, oldNewSetup)
	}
	if o.usedOldNoSetup {
		warnOld(cmd, oldNewNoSetup)
	}
}

func validateNewOpts(cmd *cobra.Command, o *newOpts) (wl string, err error) {
	if err = client.CheckSessionName(o.name); err != nil {
		return "", err
	}
	if o.in != "" {
		if err = checkInFlags(cmd); err != nil {
			return "", err
		}
	}
	wl, err = parseWallFlag(o.wallLevel)
	if err != nil {
		return "", err
	}
	if cmd.Flags().Changed("secret") {
		if o.secrets, err = parseNameList("--secret", o.secrets, false, "Pass one or more secret names from [secrets] in config.toml."); err != nil {
			return "", err
		}
	}
	if cmd.Flags().Changed("expose") {
		if o.expose, err = parseNameList("--expose", o.expose, false, "Pass one or more secret names from [secrets] in config.toml."); err != nil {
			return "", err
		}
	}
	if o.setup && o.noSetup {
		return "", &client.UserError{Msg: "--setup-script and --no-setup-script cannot go together", Hint: "Pass one of them.", Code: "flags_conflict", Kind: client.KindUsage}
	}
	return wl, nil
}

// checkInFlags refuses sandbox-changing flags when joining an existing sandbox
// with `new --in` (isolation spec 6.11).
func checkInFlags(cmd *cobra.Command) error {
	for _, flag := range []string{
		"wall", "dir", "old-dir", "mkdir", "worktree", "base",
		"cpus", "memory", "memory-max", "copy", "seed", "seed-dirty",
		"image", "keep-system", "docker",
		"setup-script", "no-setup-script", "setup", "no-setup",
	} {
		if cmd.Flags().Changed(flag) {
			shownFlag := flag
			if shownFlag == "old-dir" {
				shownFlag = "dir"
			}
			return &client.UserError{
				Msg:  fmt.Sprintf("--%s cannot be used with --in", shownFlag),
				Hint: "Remove --" + shownFlag + ": a session joining a sandbox inherits the sandbox's wall, folder and policy.",
				Code: "flags_conflict",
				Kind: client.KindUsage,
			}
		}
	}
	return nil
}

func applyNewIn(ctx context.Context, cmd *cobra.Command, conn *client.Conn, hostName, raw string, a *client.NewArgs) error {
	inID, err := resolveInSession(ctx, cmd, conn, hostName, raw)
	if err != nil {
		return err
	}
	a.In = inID
	a.Cwd = ""
	if len(a.Argv) > 0 && !cmd.Flags().Changed("agent") {
		a.Agent = ""
	}
	return nil
}

func resolveInSession(ctx context.Context, cmd *cobra.Command, conn *client.Conn, hostName, raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if h, rest, ok := strings.Cut(ref, "/"); ok {
		if h != hostName {
			return "", &client.UserError{
				Msg:  fmt.Sprintf("--in %q names host %q, not %q", raw, h, hostName),
				Hint: fmt.Sprintf("Use %s new %s --in %s", brand.Name, hostName, rest),
				Code: "host_mismatch",
				Kind: client.KindUsage,
			}
		}
		ref = rest
	}
	list, err := listSessions(ctx, conn)
	if err != nil {
		return "", err
	}
	s, err := findIn(cmd, list, ref, hostName) //nolint:contextcheck // its Try: line parses a command line, which builds the command tree and runs nothing
	if err != nil {
		return "", err
	}
	return s.ID, nil
}
