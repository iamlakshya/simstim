package host

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// DecodePolicyReplace accepts one complete closed source packet. The current
// supervisor alone owns the hash, excluded layers and actual live lifetime.
func DecodePolicyReplace(raw json.RawMessage) (api.PolicyReplaceArgsV1, error) {
	var args api.PolicyReplaceArgsV1
	if !api.ExactJSONFields(raw, args) {
		return args, api.ErrPolicyLiveV1
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || decoder.Decode(&struct{}{}) != io.EOF || args.Validate() != nil {
		return api.PolicyReplaceArgsV1{}, api.ErrPolicyLiveV1
	}
	return args, nil
}

func dispatchPolicyReplace(raw json.RawMessage) (any, error) {
	args, err := DecodePolicyReplace(raw)
	if err != nil {
		return nil, UserError("bad_request", "invalid live policy replacement", "Use matching helpers and refresh the selected policy layers.")
	}
	if !alive(args.Session) {
		return nil, UserError("policy_not_running", "the sandbox has no running policy service", "Apply the policy after starting the sandbox, or let its next start read the files.")
	}
	return HolderRequestWithFeatures(args.Session, "policy.replace", args, 5*time.Second, api.FeaturePolicyLiveV1)
}
