package inputtransfer

import "github.com/iamlakshya/simstim/internal/proto"

// PrepareArgs reserves one input phase in the existing control reader. Start
// is infrastructure only for initial boot; nil uses an already prepared guest
// for pending copies. It never carries an agent command or secret values.
type PrepareArgs struct {
	Control Control             `json:"control"`
	Start   *proto.CtlStartArgs `json:"start,omitempty"`
}
