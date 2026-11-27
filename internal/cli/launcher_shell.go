package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/tui"
)

// This is an adapter to the same shell body as shellCmd, not another grant or
// sandbox implementation. Only an actual accepted Conn can restrict its dials.
func runLauncherSessionShell(ctx context.Context, accepted tui.Conn, host, id string) (func(*os.File, *os.File) error, func() error, error) {
	parent, ok := accepted.(*client.Conn)
	if !ok || parent == nil || parent.Host.Name != host || !client.ValidHostName(host) || !api.ValidSessionID(id) {
		return nil, nil, &client.UserError{Msg: "the shell action lost its original host and exact session",
			Hint: "Reconnect the host and choose the session again.", Code: "host_changed", Kind: client.KindRefused}
	}
	bound, closeScope, err := parent.RestrictSavedHostSourceV1(ctx)
	if err != nil {
		return nil, closeScope, err
	}
	run := func(in, out *os.File) error {
		selected := &shellCommandSelection{address: address{host: parent.Host, ref: id, byID: true}, in: in, out: out}
		command := launcherShellCommand(bound, selected)
		return command.RunE(command, []string{host + "/" + id})
	}
	return run, closeScope, nil
}

func launcherShellCommand(ctx context.Context, selected *shellCommandSelection) *cobra.Command {
	command := shellCommand(selected)
	command.SetIn(selected.in)
	command.SetOut(selected.out)
	command.SetErr(selected.out)
	terminal := client.WithCommandTerminalV1(ctx, selected.in, selected.out)
	command.SetContext(context.WithValue(terminal, promptTerminalKey{}, promptTerminalFiles{selected.in, selected.out}))
	return command
}

// Global lockdown keeps its existing real CLI host selection and guarded
// per-host writer. The palette contributes no new approval or actor Source.
func runLauncherPolicyLockdown(ctx context.Context, in, out *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := policyLockdownCmd()
	command.SetIn(in)
	command.SetOut(out)
	command.SetErr(out)
	command.SetContext(ctx)
	return command.RunE(command, nil)
}
