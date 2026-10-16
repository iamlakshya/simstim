// Package cli is the client's command tree: the launcher and every command.
// The binary's main (main.go at the repository root) calls Main, and
// cmd/docgen builds the reference docs from Root.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/version"
)

// Exit codes.
const (
	exitUser    = 1
	exitHost    = 2
	exitMissing = 3
)

// Main runs the client with the process's arguments and returns its exit
// code.
func Main() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	err := Root().ExecuteContext(ctx)
	if err != nil {
		client.DebugLog().Warn("the command failed", "err", err)
	}
	// The debug files are complete before the process ends.
	client.StopDebug(os.Stderr)
	if err != nil {
		return printError(err)
	}
	return 0
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
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHome(cmd.Context())
		},
	}
	root.AddCommand(setupCmd(), hostCmd(), newCmd(), lsCmd(), agentsCmd(), attachCmd(), resumeCmd(), flipCmd(), sendCmd(), killCmd(), renameCmd(), portsCmd(), forwardCmd(), pullCmd(), doctorCmd(), debugCmd(), configCmd(), demoCmd(), versionCmd())
	addDebugFlags(root)
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

// printError prints an error with its suggested next step, and returns the exit code.
func printError(err error) int {
	var ue *client.UserError
	red, blue, bold, reset := "\x1b[31m", "\x1b[34m", "\x1b[1m", "\x1b[0m"
	if os.Getenv("NO_COLOR") != "" || !isTerminal(os.Stderr) {
		red, blue, bold, reset = "", "", "", ""
	}
	if errors.As(err, &ue) {
		fmt.Fprintf(os.Stderr, "%s%sError:%s %s\n", bold, red, reset, ue.Msg)
		if ue.Hint != "" {
			fmt.Fprintf(os.Stderr, "\n%sTry:%s %s\n", blue, reset, ue.Hint)
		}
		if ue.Hint != "" && containsAny(ue.Msg, "cannot reach", "connection") {
			return exitHost
		}
		if containsAny(ue.Msg, "no session", "no host") {
			return exitMissing
		}
		return exitUser
	}
	if errors.Is(err, client.ErrClosed) {
		fmt.Fprintf(os.Stderr, "%s%sError:%s the connection to the host closed\n\n%sTry:%s run the command again\n", bold, red, reset, blue, reset)
		return exitHost
	}
	fmt.Fprintf(os.Stderr, "%s%sError:%s %v\n", bold, red, reset, err)
	return exitUser
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if len(x) > 0 && len(s) >= len(x) {
			for i := 0; i+len(x) <= len(s); i++ {
				if s[i:i+len(x)] == x {
					return true
				}
			}
		}
	}
	return false
}
