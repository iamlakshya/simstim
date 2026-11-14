package client

// retainedHostStateError is an owner decision after complete schema checking.
// It grants no unlink authority. Native/root/sentinel validation still belongs
// to the one state engine and takes precedence over this classification.
type retainedHostStateError struct{ cause error }

func (e *retainedHostStateError) Error() string { return e.cause.Error() }
func (e *retainedHostStateError) Unwrap() error { return e.cause }

// RetainHostState marks known validated metadata which its canonical owner
// requires keeping. The removal result reports Retained and incomplete. An
// unknown schema, malformed data or unsafe native object must not use this.
func RetainHostState(err error) error {
	if err == nil {
		return nil
	}
	return &retainedHostStateError{cause: err}
}
