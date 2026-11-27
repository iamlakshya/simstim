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
		Use: "stop <host>/<session>", Short: "Stop a sandbox and keep its work and conversation",
		Example:           "  " + brand.Name + " stop my-vps/auth-fix\n  " + brand.Name + " stop my-vps/auth-fix --json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
				stopped, err := c.Stop(ctx, s.ID)
				if err != nil {
					return err
				}
				client.RememberSession(h.Name, *stopped)
				if asJSON {
					return printJSON(sessionObject(h.Name, *stopped))
				}
				say(cmd, "Stopped %s on %s. Its work stays.\n", client.CleanLine(s.Name), client.CleanLine(h.Name))
				return nil
			})
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}
