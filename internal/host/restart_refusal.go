package host

import (
	"errors"
	"github.com/iamlakshya/simstim/internal/proto"
)

// restartRefusal keeps actionable final-gate room data and command refusal
// hints intact. The final native save can refuse after an earlier room check.
func restartRefusal(err error) *proto.Error {
	var refused *proto.Error
	if errors.As(err, &refused) && (refused.Code == "no_room" || refused.Code == dangerRefused) {
		return refused
	}
	return nil
}
