package cli

import (
	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

func optionalNewHost(cmd *cobra.Command, args []string) error {
	before := len(args)
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		before = dash
	}
	if before > 1 {
		return oneHost(cmd, args)
	}
	return nil
}

func selectedNewHost(cmd *cobra.Command, args []string) (client.Host, error) {
	before := len(args)
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		before = dash
	}
	selected := ""
	if before == 1 {
		selected = args[0]
	}
	return client.ResolveHost(cmd.Context(), selected)
}
