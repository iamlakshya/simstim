package host

import (
	"bytes"

	"github.com/iamlakshya/simstim/internal/api"
)

func validateResumeInputShape(raw []byte) error {
	fields, err := api.JSONObjectFields(raw, "inputs")
	if err != nil {
		return UserError("bad_request", "resume inputs are ambiguous", "Prepare a fresh local input request with matching client and helper versions.")
	}
	if value, present := fields["inputs"]; present && (bytes.Equal(bytes.TrimSpace(value), []byte("null")) || !api.ExactJSONFields(raw, api.ResumeArgs{})) {
		return UserError("bad_request", "resume has an unsupported input declaration", "Use a supported explicit local input plan.")
	}
	return nil
}
