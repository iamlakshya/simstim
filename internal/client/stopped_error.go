package client

import (
	"context"
	"errors"
)

// StoppedError is the error of a change that a cancel or a timeout came
// before: nothing was changed. It is a UserError, of KindCancelled after a
// cancel, and it is the context's error too, for errors.Is.
type StoppedError struct {
	*UserError
	Err error
}

// Unwrap gives the UserError and the context's error.
func (e *StoppedError) Unwrap() []error { return []error{e.UserError, e.Err} }

// stoppedBefore is the StoppedError of what, which err stopped.
func stoppedBefore(what string, err error) error {
	ue := &UserError{Msg: "cancelled before it could " + what + "; nothing was changed", Hint: "Run the command again.", Code: "cancelled", Kind: KindCancelled}
	if errors.Is(err, context.DeadlineExceeded) {
		ue = &UserError{Msg: "timed out before it could " + what + "; nothing was changed", Hint: "Run the command again.", Code: "timeout"}
	}
	return &StoppedError{UserError: ue, Err: err}
}
