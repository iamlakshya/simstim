package supervisor

import (
	"encoding/json"
	"io"
	"math"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// checkSecretPublicResponse uses the actual public Arm result and ordinary
// response envelope. Independent init request/result fits are insufficient.
func checkSecretPublicResponse(state *api.SessionSecretState) error {
	if state == nil || !state.Supported() {
		return secretIntentFailure()
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return secretCommitFailure()
	}
	if err := proto.NewWriter(io.Discard).WriteJSON(proto.TResponse, proto.Response{ID: math.MaxUint64, OK: true, Result: payload}); err != nil {
		return &proto.Error{Code: "state_too_large", Message: "the complete public secret response exceeds the control frame limit",
			Hint: "Use only the required approved secret names and policy before starting a fresh sandbox."}
	}
	return nil
}
