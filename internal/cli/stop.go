package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func stopCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stop <host>/<session>",
		Short: "Stop a session and keep its state for resume or restore",
		Long: "Stops a session and its systemd user unit on the host, while keeping its folder,\n" +
			"worktree and conversation so it can be resumed or restored later. Use rm to\n" +
			"stop a session and remove its files.",
		Example: "  " + brand.Name + " stop my-vps/auth-fix\n" +
			"  " + brand.Name + " stop my-vps/auth-fix --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				res, err := conn.Stop(ctx, s.ID)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(sessionObject(h.Name, res.Session))
				}
				d := client.CleanSession(res.Session)
				if res.AlreadyStopped {
					say(cmd, "%s (%s) on %s is already stopped.\n", d.Name, d.ID, h.Name)
					return nil
				}
				say(cmd, "Stopped %s (%s) on %s. Resume it with %s resume %s.\n",
					d.Name, d.ID, h.Name, brand.Name, client.CommandTarget(h.Name, res.Session.ID))
				return nil
			})
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}
