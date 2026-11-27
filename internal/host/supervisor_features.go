package host

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// HolderRequestWithFeatures checks the actual running session before writing a
// constrained request. An upgraded bridge cannot speak for an older supervisor.
func HolderRequestWithFeatures(id, op string, args any, timeout time.Duration, required ...string) (json.RawMessage, error) {
	return holderRequest(id, op, args, timeout, func(h proto.Hello) error { return requireSupervisorFeatures(h, required...) })
}
func requireSupervisorFeatures(h proto.Hello, required ...string) error {
	if h.Check(version.Protocol) != nil {
		return UserError("upgrade_required", "the session reported an incompatible handshake", "Stop and resume the session with the current helper.")
	}
	for _, feature := range required {
		if !slices.Contains(h.Features, feature) {
			return UserError("upgrade_required", "the running session lacks "+feature, "Stop and resume the session with the current helper.")
		}
	}
	return nil
}

func checkSessionFeatures(id string, timeout time.Duration, required ...string) error {
	line, hello, err := dialSessionHolder(id, timeout)
	if err != nil {
		return err
	}
	defer line.c.Close()
	return requireSupervisorFeatures(hello, required...)
}
