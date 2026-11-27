package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// ResumePreparationV1 is protected accounting DATA for the original cold
// preparation. Nonce and metadata cannot create an OP, native stop/artifact
// source, reservation issuer, boot, selection or capacity permission.
type ResumePreparationV1 struct {
	V                 int       `json:"v"`
	Nonce             string    `json:"nonce"`
	Boot              string    `json:"boot"`
	Since             time.Time `json:"since"`
	MemoryMax         int64     `json:"memory_max"`
	PreviousMemoryMax int64     `json:"previous_memory_max,omitempty"`
	opaque            json.RawMessage
}

func (ResumePreparationV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}
func resumePreparationDataErrorV1() error {
	return UserError("resume_preparation", "the protected cold preparation is invalid or unsupported", "Retain its original operation and native resources; inspect them with the matching helper before retrying.")
}
func (value *ResumePreparationV1) UnmarshalJSON(raw []byte) error {
	fields, err := api.JSONObjectFields(raw, "v", "nonce", "boot", "since", "memory_max", "previous_memory_max")
	if err != nil {
		return resumePreparationDataErrorV1()
	}
	var version int
	if len(fields["v"]) == 0 || json.Unmarshal(fields["v"], &version) != nil || version < 1 {
		return resumePreparationDataErrorV1()
	}
	if version != 1 {
		*value = ResumePreparationV1{V: version, opaque: bytes.Clone(raw)}
		return nil
	}
	type wire ResumePreparationV1
	if len(fields) != 5 && len(fields) != 6 || !api.ExactJSONFields(raw, wire{}) {
		return resumePreparationDataErrorV1()
	}
	var decoded wire
	if json.Unmarshal(raw, &decoded) != nil {
		return resumePreparationDataErrorV1()
	}
	*value = ResumePreparationV1(decoded)
	return validateResumePreparationV1Data(value)
}
func (value ResumePreparationV1) MarshalJSON() ([]byte, error) {
	if len(value.opaque) != 0 {
		return bytes.Clone(value.opaque), nil
	}
	if err := validateResumePreparationV1Data(&value); err != nil {
		return nil, err
	}
	type wire ResumePreparationV1
	return json.Marshal(wire(value))
}
func cloneResumePreparationV1(input *ResumePreparationV1) *ResumePreparationV1 {
	if input == nil {
		return nil
	}
	copy := *input
	copy.opaque = bytes.Clone(input.opaque)
	return &copy
}
func validateResumePreparationV1Data(value *ResumePreparationV1) error {
	if value == nil {
		return nil
	}
	if value.V != 1 || len(value.opaque) != 0 || len(value.Nonce) != 64 || strings.Trim(value.Nonce, "0123456789abcdef") != "" || !auxiliaryBootIDV1(value.Boot) || value.Since.IsZero() || value.Since.Location() != time.UTC || value.MemoryMax <= 0 {
		return resumePreparationDataErrorV1()
	}
	if value.PreviousMemoryMax != 0 {
		if value.PreviousMemoryMax == value.MemoryMax {
			return resumePreparationDataErrorV1()
		}
		if err := checkExactWalledCapV1(value.PreviousMemoryMax); err != nil {
			return err
		}
	}
	return checkExactWalledCapV1(value.MemoryMax)
}
