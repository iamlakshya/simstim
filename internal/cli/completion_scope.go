package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
)

func completionContext(cmd *cobra.Command) context.Context {
	if cmd != nil && cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}

func completeInFlag(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	rows, directive := completeSessions(cmd, args, prefix)
	// A fresh agent in an existing sandbox must use the explicit host of
	// new. Policy commands have no positional host and retain all cached rows.
	if cmd == nil || cmd.Name() != "new" || len(args) == 0 {
		return rows, directive
	}
	var out []string
	for _, row := range rows {
		address, _, _ := strings.Cut(row, "\t")
		if strings.HasPrefix(address, args[0]+"/") {
			out = append(out, row)
		}
	}
	return out, directive
}
