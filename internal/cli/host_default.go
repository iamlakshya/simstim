package cli

import (
	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func hostDefaultCmd() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:     "default [<host>]",
		Short:   "Show or choose the default host for new sessions",
		Example: "  " + brand.Name + " host default\n  " + brand.Name + " host default my-vps\n  " + brand.Name + " host default --clear",
		Args:    cobra.MaximumNArgs(1), ValidArgsFunction: completeHostArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear && len(args) != 0 {
				return policyUsage("--clear takes no host name", "Pass host default --clear, or host default <host>.", "flags_conflict")
			}
			if clear {
				return client.SetDefaultHost(cmd.Context(), "")
			}
			if len(args) == 1 {
				return client.SetDefaultHost(cmd.Context(), args[0])
			}
			name, err := client.DefaultHost(cmd.Context())
			if err != nil {
				return err
			}
			if name == "" {
				say(cmd, "No default host is selected.\n")
			} else {
				say(cmd, "%s\n", shown(name))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "clear the saved default host")
	return cmd
}
