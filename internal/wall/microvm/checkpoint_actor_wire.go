package microvm

import (
	"time"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/proto"
)

const checkpointActorFrameV3 = 256 << 10

// These are bounded operation data on the SAME original native B stream.
// No action, timestamp, cursor or layout field constructs an actor or Source.
type checkpointActorRequestV3 struct {
	Version  int                 `json:"v"`
	Action   string              `json:"action"`
	When     time.Time           `json:"when,omitempty"`
	Metadata checkpoint.Metadata `json:"metadata,omitempty"`
	Entry    *checkpoint.Entry   `json:"entry,omitempty"`
	Keep     int                 `json:"keep,omitempty"`
	Needed   int64               `json:"needed,omitempty"`
	After    string              `json:"after,omitempty"`
}

type checkpointActorReplyV3 struct {
	Version int               `json:"v"`
	Action  string            `json:"action"`
	Entry   *checkpoint.Entry `json:"entry,omitempty"`
	Error   *proto.Error      `json:"error,omitempty"`
}
