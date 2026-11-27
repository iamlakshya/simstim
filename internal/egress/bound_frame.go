package egress

import (
	"encoding/json"
	"io"
	"math"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Count the actual typed initial request, including transient fresh values
// and format provenance, before any sentinel entropy is consumed.
func checkBoundInitialFrame(args BoundInitArgs) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return registryIntentError()
	}
	if proto.NewWriter(io.Discard).WriteJSON(proto.TRequest, proto.Request{ID: math.MaxUint64, Op: CtlBoundInit, Args: raw}) != nil {
		return memberError("state_too_large", "complete bound initial state exceeds the control frame limit", "Use only the required approved names, selected sources and policy.")
	}
	return nil
}

func checkBoundControlResult(result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return registryIntentError()
	}
	if proto.NewWriter(io.Discard).WriteJSON(proto.TResponse, proto.Response{ID: math.MaxUint64, OK: true, Result: raw}) != nil {
		return memberError("state_too_large", "complete bound result exceeds the control frame limit", "Use only the required approved names and public state before retrying.")
	}
	return nil
}
