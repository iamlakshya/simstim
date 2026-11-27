package onboard

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/client"
)

type partialCreationError struct {
	*client.UserError
	cause error
}

func (e *partialCreationError) Unwrap() []error { return []error{e.UserError, e.cause} }

// Report the actual returned value without inferring successful creation,
// publication, private proof readiness, resource absence or cleanup authority.
func partialCreationResult(ctx context.Context, actual client.Host, cause error) error {
	const defaultHint = "Inspect this actual provider operation and its returned resource before retrying or deleting anything."
	out := &client.UserError{Code: "provider_partial", Kind: client.KindFailed, Hint: defaultHint}
	var original *client.UserError
	if errors.As(cause, &original) {
		copy := *original
		out = &copy
	}
	if errors.Is(cause, context.Canceled) {
		out.Code, out.Kind = "cancelled", client.KindCancelled
	}
	what := fmt.Sprintf("host %q", client.CleanLine(actual.Name))
	if actual.Machine != nil {
		what = fmt.Sprintf("%s resource %q for host %q", client.CleanLine(actual.Machine.Provider), client.CleanLine(actual.Machine.ID), client.CleanLine(actual.Name))
	}
	out.Msg = fmt.Sprintf("%v; the provider returned %s, which may remain. Saving and automatic deletion have not been confirmed", cause, what)
	if ctx.Err() != nil {
		out.Msg += "; the creation caller also ended"
	}
	out.Hint = defaultHint
	return &partialCreationError{UserError: out, cause: cause}
}
