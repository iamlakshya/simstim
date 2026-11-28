package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// execCmd runs one non-interactive command inside a walled session's sandbox
// (isolation spec 6.11, CLI spec 5.2).
func execCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "exec <host>/<session> [--timeout <duration>] -- <command>…",
		Short: "Run one command inside a session's sandbox",
		Long: "Runs one command without a terminal inside a walled session's sandbox, keeping\n" +
			"stdout and stderr apart and exiting with the command's own exit code. Exits with\n" +
			"124 on timeout, 125 on a failure before the command starts, 126 when the command\n" +
			"cannot run, 127 when it is not found, and 128+N when signal N ends it.",
		Example: "  " + brand.Name + " exec my-vps/auth-fix -- npm test\n" +
			"  " + brand.Name + " exec my-vps/auth-fix --timeout 2m -- go test ./...",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(cmd, args, timeout)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "limit how long the command may run (0 disables the limit)")
	return cmd
}

// execRunner runs one exec call on h. Tests may replace it.
var execRunner = client.Exec

func runExec(cmd *cobra.Command, args []string, timeout time.Duration) error {
	if timeout < 0 {
		return &client.UserError{
			Msg:  fmt.Sprintf("--timeout must be zero or positive, not %s", timeout),
			Hint: "Pass a duration such as 2m or 10m.",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	dash := cmd.ArgsLenAtDash()
	if dash < 1 || dash >= len(args) {
		return &client.UserError{
			Msg:  "exec needs a command after --",
			Hint: fmt.Sprintf("%s exec %s -- <command>", brand.Name, args[0]),
			Code: "missing_command",
			Kind: client.KindUsage,
		}
	}
	if dash > 1 {
		return &client.UserError{
			Msg:  fmt.Sprintf("exec takes one session before --, and got %d words", dash),
			Hint: brand.Name + " exec <host>/<session> -- <command>",
			Code: "bad_args",
			Kind: client.KindUsage,
		}
	}
	argv := args[dash:]

	a, err := resolveAddress(cmd, args[0], false)
	if err != nil {
		return err
	}
	var host client.Host
	var target client.Session
	var liveList []client.Session
	if err := a.open(cmd.Context(), cmd, func(_ context.Context, f found) error {
		host = f.host
		target = *f.s
		liveList = f.list
		return nil
	}); err != nil {
		return err
	}
	if target.Wall == nil || target.Wall.Level == "" || target.Wall.Level == "none" {
		return &client.UserError{
			Msg:  fmt.Sprintf("%s on %s has no sandbox wall", shown(nameOf(target)), shown(host.Name)),
			Hint: "Start a walled session with --wall process, or use ssh to run commands on the host.",
			Code: "not_walled",
		}
	}
	sandbox := target
	if target.SandboxOf != "" {
		if primary := byID(liveList, target.SandboxOf); primary != nil {
			sandbox = *primary
		}
	}
	if !sandbox.Alive || sandbox.State == api.StateStopped {
		return &client.UserError{
			Msg:  fmt.Sprintf("sandbox %s on %s is stopped", shown(nameOf(sandbox)), shown(host.Name)),
			Hint: fmt.Sprintf("%s resume %s", brand.Name, client.CommandTarget(host.Name, sandbox.ID)),
			Code: "sandbox_stopped",
		}
	}

	var stdin io.Reader
	if !isTerminal(os.Stdin) {
		stdin = os.Stdin
	}
	code, err := execRunner(cmd.Context(), host, api.ExecArgs{
		ID:        target.ID,
		Argv:      argv,
		Cwd:       target.Cwd,
		TimeoutMS: timeout.Milliseconds(),
		NoTimeout: timeout == 0,
	}, stdin, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return silentError{code: code}
	}
	return nil
}
