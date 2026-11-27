package supervisor

import "errors"

// ctlUnsentError is an internal transport fact: this request never entered
// the control writer. A remote error code or claim cannot construct it.
type ctlUnsentError struct{ cause error }

func (e *ctlUnsentError) Error() string { return e.cause.Error() }
func (e *ctlUnsentError) Unwrap() error { return e.cause }
func controlWasNotSent(err error) bool  { var unsent *ctlUnsentError; return errors.As(err, &unsent) }
