package host

import (
	"bytes"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

func validateJoinSecretShape(raw []byte) error {
	fields, err := api.JSONObjectFields(raw, "secret_delivery")
	if err != nil {
		return UserError("bad_secret_delivery", "member secret intent is ambiguous", "Use matching helpers and the original fresh member request.")
	}
	if intent, present := fields["secret_delivery"]; present {
		if bytes.Equal(bytes.TrimSpace(intent), []byte("null")) || !api.ExactJSONFields(raw, (*api.JoinArgs)(nil)) {
			return UserError("bad_secret_delivery", "member secret intent is unsupported", "Send explicit supported modes for the freshly approved names.")
		}
	}
	return nil
}

// A new-form resume cannot lose explicit intent through null, duplicate or
// case-alias conversion. Legacy resumes retain their existing decoder path.
func validateResumeSecretShape(raw []byte) error {
	fields, err := api.JSONObjectFields(raw, "secret_delivery", "members")
	if err != nil {
		return UserError("bad_request", "resume needs unambiguous arguments", "Use a matching client and a fresh complete sandbox snapshot.")
	}
	modern := false
	if intent, present := fields["secret_delivery"]; present {
		modern = true
		if bytes.Equal(bytes.TrimSpace(intent), []byte("null")) {
			return UserError("bad_secret_delivery", "resume has no supported secret intent", "Use explicit modes for the original locally approved names.")
		}
	}
	var members []json.RawMessage
	if encoded := fields["members"]; encoded != nil && json.Unmarshal(encoded, &members) != nil {
		return UserError("bad_request", "resume members are malformed", "Read a fresh complete sandbox snapshot.")
	}
	for _, member := range members {
		memberFields, err := api.JSONObjectFields(member, "secret_delivery")
		if err != nil {
			return UserError("bad_secret_delivery", "member secret intent is ambiguous", "Use matching helpers and the complete original member set.")
		}
		if intent, present := memberFields["secret_delivery"]; present {
			modern = true
			if bytes.Equal(bytes.TrimSpace(intent), []byte("null")) {
				return UserError("bad_secret_delivery", "member secret intent is missing", "Use explicit original modes for each member.")
			}
		}
	}
	if modern && !api.ExactJSONFields(raw, (*api.ResumeArgs)(nil)) {
		return UserError("bad_secret_delivery", "resume has unsupported secret metadata", "Use matching helpers and explicit original delivery modes.")
	}
	return nil
}
