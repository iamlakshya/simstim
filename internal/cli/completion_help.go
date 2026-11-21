package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// Help topics are intentionally hidden from ordinary command help. They still
// complete after help, alongside the actual current tree's visible commands.
func completeHelpArgs(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if cmd == nil || completionContext(cmd).Err() != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	at := cmd.Root()
	for _, word := range args {
		var next *cobra.Command
		for _, child := range at.Commands() {
			if child.Name() == word && (!child.Hidden || IsTopic(child)) {
				next = child
				break
			}
		}
		if next == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		at = next
	}
	var out []string
	for _, child := range at.Commands() {
		if (!child.Hidden || IsTopic(child)) && strings.HasPrefix(child.Name(), prefix) {
			out = append(out, compItem(child.Name(), cleanCompDesc(child.Short)))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
