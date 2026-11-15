package host

import (
	"bytes"
	"encoding/json"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// decodeNewArgs keeps a present malformed or duplicate wall request from
// becoming the zero value before the typed start interlock can inspect it.
func decodeNewArgs(raw json.RawMessage) (NewArgs, error) {
	var args NewArgs
	fields, err := api.JSONObjectFields(raw, "wall", "secret_delivery", "runtime", "vm_need_v1", "inputs", "environment")
	if err != nil {
		return args, UserError("bad_request", "the new request is not an unambiguous JSON object", "Update the client and send the request again.")
	}
	if !api.ExactJSONFields(raw, (*api.NewArgs)(nil)) {
		return args, UserError("bad_request", "new needs exact supported request fields", "Use a matching client and its original sealed start request.")
	}
	if value, present := fields["runtime"]; present {
		var original supply.Manifest
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || !api.ExactJSONFields(value, original) || json.Unmarshal(value, &original) != nil {
			return args, UserError("bad_runtime", "runtime must be an exact supported original manifest", "Prepare the runtime through the same current client request.")
		}
		if err := original.Validate(); err != nil {
			return args, UserError("bad_runtime", "the original runtime manifest is invalid", "Refresh the signed runtime through the original client request.")
		}
	}
	if value, present := fields["vm_need_v1"]; present {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return args, UserError("bad_runtime", "VM Need cannot be null", "Send the original supported client Need, or choose an ordinary wall.")
		}
		if _, err := vmneed.DecodeV1(value, runtime.GOARCH); err != nil {
			return args, UserError("bad_runtime", "the original VM Need is unsupported or malformed", "Prepare a supported Need through the original client request.")
		}
	}
	if wall, present := fields["wall"]; present {
		var level string
		if bytes.Equal(bytes.TrimSpace(wall), []byte("null")) || json.Unmarshal(wall, &level) != nil {
			return args, UserError("bad_wall", "the requested wall must be a string", "Choose auto, none, process, gvisor or microvm.")
		}
		if err := checkRequestedWall(level); err != nil {
			return args, err
		}
	}
	if delivery, present := fields["secret_delivery"]; present {
		if bytes.Equal(bytes.TrimSpace(delivery), []byte("null")) || !api.ExactJSONFields(raw, (*api.NewArgs)(nil)) {
			return args, UserError("bad_secret_delivery", "secret delivery must be an exact supported object", "Use matching helpers and explicit delivery modes.")
		}
	}
	if value, present := fields["inputs"]; present && (bytes.Equal(bytes.TrimSpace(value), []byte("null")) || !api.ExactJSONFields(value, api.InputSummary{})) {
		return args, UserError("bad_request", "invalid protected input declaration", "Prepare the local inputs again.")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, UserError("bad_request", "the new request has an invalid value", "Update the client and send the request again.")
	}
	return args, nil
}
