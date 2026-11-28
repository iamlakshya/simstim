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
	args := os.Args[1:]
	cmd, err := Root().ExecuteContextC(withLine(ctx, args))
	// ctx ends only at a signal.
	signalled := ctx.Err() != nil
	if err != nil {
		client.DebugLog().Warn("the command failed", "err", err)
	}
	// The debug files are complete before the process ends.
	asJSON := jsonAsked(cmd, args)
	stopDebug(stderrFor(asJSON, "debug_incomplete"))
	return finish(os.Stderr, err, cmd, colourAsked(cmd, args, os.Stderr), signalled, asJSON)
}

// stopDebug ends the client's debug run, and writes any warning on its
// writer. Tests replace it, to make it warn.
var stopDebug = client.StopDebug

// Root builds the command tree.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   brand.Name,
		Short: "Run any coding agent on any host, and use it as if it ran here",
		// The reference index and the man page print Long. The root help
		// says the same in its usage line, and leaves Long out.
		Long:          rootLong,
		Example:       rootExample(),
		Version:       fmt.Sprintf("%s (protocol %s)", version.Version, version.Protocol),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          unknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHome(cmd)
		},
	}
	root.AddCommand(setupCmd(), hostCmd(), newCmd(), lsCmd(), agentsCmd(), attachCmd(), resumeCmd(), stopCmd(), flipCmd(), sendCmd(), killCmd(), renameCmd(), portsCmd(), forwardCmd(), pullCmd(), doctorCmd(), debugCmd(), configCmd(), demoCmd(), versionCmd())
	root.AddCommand(topics()...)
	addRootFlags(root)
	addDebugFlags(root)
	// Cobra adds these two itself when it runs, after guard would have
	// looked: add them now, so that they get the same errors and help.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	teach(root)
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			c.Args = helpArgs
		}
	}
	guard(root)
	return root
}

func versionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "version",
		Short:   "Print the version",
		Example: "  " + brand.Name + " version\n  " + brand.Name + " version --json",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if asJSON {
				return printJSON(map[string]string{"version": version.Version, "protocol": version.Protocol, "helper": client.HelperVersion()})
			}
			fmt.Printf("%s %s (protocol %s, host helper %s)\n", brand.Name, version.Version, version.Protocol, client.HelperVersion())
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}
