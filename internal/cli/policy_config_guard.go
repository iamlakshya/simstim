package cli

import "github.com/spf13/cobra"

// Commands which can send layers must fail with the strict settings error.
// Read-only commands retain C9's single warning and never send new layers.
func policyWritesSettings(cmd *cobra.Command) bool {
	switch shortPath(cmd) {
	case "new", "resume", "fork", "image pull", "policy allow", "policy deny", "policy rm", "policy apply", "policy edit", "policy learn":
		return true
	default:
		return false
	}
}
