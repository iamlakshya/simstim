// Package cli is the client's command tree: the launcher and every command.
// The binary's main (main.go at the repository root) calls Main, and
// cmd/docgen builds the reference docs from Root.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/version"
)

// Main runs the client with the process's arguments and returns its exit
// code, which comes from the kind of the error (docs/reference/exit-codes.md).
func Main() int {
	if code, handled := client.GitSSHMode(os.Args[1:]); handled {
		return code
	}
	ctx, stop := listen()
	defer stop()
	if os.Getenv(brand.Env("NO_INSTALL")) == "1" {
		ctx = client.WithoutInstall(ctx)
	}
	args := os.Args[1:]
	completion := completionArgs(args)
	if completion {
		if len(args) == 1 {
			_, _ = fmt.Fprintln(os.Stdout, ":4")
			return 0
		}
		restore, ok := quietCompletionErrors()
		if !ok {
			_, _ = fmt.Fprintln(os.Stdout, ":4")
			return 0
		}
		defer restore()
	}
	// A wait of the client, such as for the lock of hosts.toml, or a
	// changed host ID, says so on stderr: with --json, as a warning line.
	wantJSON := jsonAsked(nil, args)
	run := client.WithNotes(withLine(ctx, args), stderrFor(wantJSON, "waiting"))
	run = client.WithWarnings(run, warningFor(wantJSON))
	cmd, err := Root().ExecuteContextC(run)
	// ctx ends only at a signal.
	signalled := ctx.Err() != nil
	if completion || isCompletionRequest(cmd) {
		if signalled {
			return exitCode(client.KindCancelled)
		}
		if err != nil {
			return exitCode(client.KindFailed)
		}
		return 0
	}
	asJSON := jsonAsked(cmd, args)
	// A run that ended before its command ran, as -v does, still warns
	// about the old forms that it used.
	warnOldFlags(cmd)
	if err != nil {
		client.DebugLog().Warn("the command failed", "err", err)
	}
	// The debug files are complete before the process ends.
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
	root.AddCommand(setupCmd(), hostCmd(), newCmd(), lsCmd(), showCmd(), agentsCmd(), attachCmd(), resumeCmd(), flipCmd(), sendCmd(), rmCmd(), killCmd(), renameCmd(), portsCmd(), forwardCmd(), pullCmd(), doctorCmd(), debugCmd(), configCmd(), demoCmd(), versionCmd())
	root.AddCommand(topics()...)
	addRootFlags(root)
	addDebugFlags(root)
	root.PersistentPreRunE = beforeRun
	// --version has no short form: Cobra adds -v only when the root has no
	// flag called version (docs/contributing/cli-spec.md 10.1).
	var showVersion bool
	root.Flags().BoolVar(&showVersion, "version", false, "print the version")
	if liveOld("-v") {
		root.Flags().BoolVarP(&showVersion, "v", "v", false, "")
		hideOld(root.Flags(), "v", "-v")
	}
	// Cobra adds these two itself when it runs, after guard would have
	// looked: add them now, so that they get the same errors and help.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	wireCompletion(root)
	teach(root)
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			c.Args = helpArgs
		}
	}
	guard(root)
	return root
}

// beforeRun runs before every command: it applies --no-color and input
// off, warns about the old forms that the command line used and of a
// config.toml that cannot be used, then turns on the debug mode that it
// asks for.
func beforeRun(cmd *cobra.Command, args []string) error {
	if isCompletionRequest(cmd) {
		cmd.Root().SetErr(io.Discard)
		return nil
	}
	applyNoColor(cmd)
	if !inputOn(cmd) {
		// Nothing asks: SSH runs with BatchMode=yes, and keeps its words
		// out of the terminal (cli-spec 6.1).
		cmd.SetContext(client.WithoutInput(cmd.Context()))
	}
	warnOldFlags(cmd)
	if !isLauncher(cmd, args) {
		cmd.SetContext(client.WithNotices(cmd.Context(), stderrFor(jsonOn(cmd), "host_key_pinned")))
		if inputOn(cmd) {
			cmd.SetContext(client.WithTierQuestion(cmd.Context(), func(ctx context.Context, h client.Host) (client.Tier, error) {
				p := prompts(cmd)
				p.ctx = ctx
				return p.askTier(h.Name)
			}))
		}
	} else {
		// The launcher and switcher show identity notices in their own chrome.
		cmd.SetContext(client.WithWarnings(cmd.Context(), nil))
	}
	warnSettings(cmd, args)
	return startDebug(cmd, args)
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
