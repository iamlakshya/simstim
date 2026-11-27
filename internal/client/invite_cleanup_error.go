package client

import "errors"

// A failed native abort retains its exact original owner in the error. Merely
// returning from Close never silently abandons a still-uncertain native stage.
type joinedStageCleanupError struct {
	stage *InviteSSHStage
	cause error
	user  *UserError
}

func (e *joinedStageCleanupError) Error() string {
	return e.user.Msg + ": " + CleanLine(e.cause.Error())
}
func (e *joinedStageCleanupError) Unwrap() error { return errors.Join(e.user, e.cause) }
func retainJoinedStage(stage *InviteSSHStage, err error) error {
	return &joinedStageCleanupError{stage: stage, cause: err, user: &UserError{Msg: "the original joined SSH cleanup did not finish", Hint: "Keep the generated directory and the original error; do not replace its ownership files.", Code: "invite_cleanup", Kind: KindFailed}}
}
