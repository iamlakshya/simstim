// Package cli is the client's command tree: the launcher and every command.
// The binary's main (main.go at the repository root) calls Main, and
// cmd/docgen builds the reference docs from Root.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/version"
)

// Main runs the client with the process's arguments and returns its exit
// code, which comes from the kind of the error (docs/reference/exit-codes.md).
func Main() int {
	ctx, stop := listen()
	defer stop()
	cmd, err := Root().ExecuteContextC(withLine(ctx, os.Args[1:]))
	// ctx ends only at a signal.
	signalled := ctx.Err() != nil
	if err != nil {
		client.DebugLog().Warn("the command failed", "err", err)
	}
	// The debug files are complete before the process ends.
	client.StopDebug(os.Stderr)
	return finish(os.Stderr, err, cmd, colourOn(os.Stderr), signalled)
}

// Root builds the command tree.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           brand.Name,
		Short:         "Run any coding agent on any host, and use it as if it ran here",
		Long:          brand.DisplayName + " runs coding agents in sessions on hosts you reach with SSH.\nSessions keep running when you leave. Run it with no arguments for the launcher.",
		Version:       fmt.Sprintf("%s (protocol %s)", version.Version, version.Protocol),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          unknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHome(cmd.Context())
		},
	}
	root.AddCommand(setupCmd(), hostCmd(), newCmd(), lsCmd(), agentsCmd(), attachCmd(), resumeCmd(), flipCmd(), sendCmd(), killCmd(), renameCmd(), portsCmd(), forwardCmd(), pullCmd(), doctorCmd(), debugCmd(), configCmd(), demoCmd(), versionCmd())
	addDebugFlags(root)
	// Cobra adds these two itself when it runs, after guard would have
	// looked: add them now, so that they get the same errors.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			c.Args = helpArgs
		}
	}
	guard(root)
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Printf("%s %s (protocol %s, host helper %s)\n", brand.Name, version.Version, version.Protocol, client.HelperVersion())
		},
	}
}
