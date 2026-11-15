package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// execOutcome preserves a command's status separately from management errors.
// It is also the outcome type of the exec command's existing implementation.
type execOutcome struct {
	code  int
	cause error
}

func (e *execOutcome) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return fmt.Sprintf("command exited with status %d", e.code)
}
func (e *execOutcome) Unwrap() error { return e.cause }

func execExit(err error) (*execOutcome, bool) {
	var result *execOutcome
	ok := errors.As(err, &result)
	return result, ok
}

// execCause retains cleanup errors joined after the program's own outcome.
func execCause(err error, outcome *execOutcome) error {
	if err == outcome {
		return outcome.cause
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var causes []error
		for _, child := range joined.Unwrap() {
			causes = append(causes, execCause(child, outcome))
		}
		return errors.Join(causes...)
	}
	return err
}

func nonTerminalNewCommand(cmd *cobra.Command, args []string, detach bool) []string {
	if detach || cmd.Flags().Changed("count") || isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		return nil
	}
	dash := cmd.ArgsLenAtDash()
	if dash < 0 || dash >= len(args) {
		return nil
	}
	return append([]string(nil), args[dash:]...)
}

func runNewCommand(cmd *cobra.Command, c *client.Conn, session client.Session, argv []string) error {
	code, err := c.RunExec(cmd.Context(), api.ExecArgs{ID: session.ID, Argv: argv}, os.Stdin, os.Stdout, os.Stderr)
	// The base terminal exists only to keep the sandbox alive during exec.
	// A completed command leaves a stopped sandbox whose work remains pullable.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 60*time.Second)
	defer cancel()
	_, stopErr := c.Stop(cleanup, session.ID)
	if err = errors.Join(err, stopErr); err != nil {
		return &execOutcome{125, err}
	}
	if code != 0 {
		return &execOutcome{code, nil}
	}
	return nil
}
