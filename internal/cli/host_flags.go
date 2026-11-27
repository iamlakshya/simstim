package cli

import "github.com/spf13/cobra"

// addConfirm adds the shared typed-name answer to a destructive question.
func addConfirm(cmd *cobra.Command, value *string, usage string) {
	cmd.Flags().StringVar(value, "confirm", "", usage)
	shared(cmd.Flags(), "confirm")
}
