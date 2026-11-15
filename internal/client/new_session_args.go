package client

import (
	"context"
	"errors"
)

// PrepareNewSessionArgs applies terminal and default start settings from the
// SAME original request operation used by policy, input and secret selection.
// It reads no configured secret value and grants no remote permission.
func PrepareNewSessionArgs(ctx context.Context, a NewArgs, cols, rows int) (NewArgs, error) {
	request := grantRequestOf(ctx)
	if request == nil {
		return a, grantOperationError()
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return a, err
	}
	operation, err := request.borrowNewSettings(ctx)
	if err != nil {
		return a, err
	}
	source, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return a, err
	}
	// The keys come from terminalArgs itself, before its actual env lookups.
	// Reuse the complete canonical name visitor and native name comparison.
	err = source.CheckKeys(ctx, []string{"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION"})
	closeErr := source.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return a, err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return a, err
	}
	a, err = SessionArgs(a, cols, rows, settings)
	if err != nil {
		return a, err
	}
	return a, operation.Revalidate(ctx)
}
