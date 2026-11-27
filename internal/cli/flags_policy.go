package cli

import "github.com/spf13/cobra"

// addProfile adds --profile, a named policy profile.
func addProfile(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "profile", "", usage)
	shared(cmd.Flags(), "profile")
}

// addIn adds --in, the session which owns an operation.
func addIn(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "in", "", usage)
	shared(cmd.Flags(), "in")
}

// addDryRun adds --dry-run, which previews without changing policy.
func addDryRun(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVar(p, "dry-run", false, usage)
	shared(cmd.Flags(), "dry-run")
}

// addFollow adds --follow, which keeps reading new results.
func addFollow(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVar(p, "follow", false, usage)
	shared(cmd.Flags(), "follow")
}
