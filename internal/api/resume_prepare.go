package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// FeatureResumePreparationV1 marks the same-connection native continuation.
const FeatureResumePreparationV1 = "resume-preparation-v1"

// FeatureResumeVMSizeV1 requires the original cold VM sizing continuation.
const FeatureResumeVMSizeV1 = "resume-vm-size-v1"

// PreparedResumeV1 names an original continuation on one accepted connection.
// A token is data. Only its actual native producer may issue runtime rights.
type PreparedResumeV1 struct {
	V     int    `json:"v"`
	Token string `json:"token"`
}

// Valid checks only the closed wire shape, never permission to resume.
func (p *PreparedResumeV1) Valid() bool {
	if p == nil || p.V != 1 || len(p.Token) != 64 {
		return false
	}
	bytes, err := hex.DecodeString(p.Token)
	return err == nil && len(bytes) == 32 && hex.EncodeToString(bytes) == p.Token
}

// Format hides a continuation token from diagnostic output.
func (PreparedResumeV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

// ResumePrepareArgsV1 carries the original next-start metadata with no private
// values. Start.Secrets, member values and NamedSSHKeys must all be absent.
type ResumePrepareArgsV1 struct {
	V         int        `json:"v"`
	PrepareVM bool       `json:"prepare_vm,omitempty"`
	Start     ResumeArgs `json:"start"`
}

// ResumePrepareResultV1 describes a genuine native preparation. Neither Mode
// nor the other fields can substitute for its retained native owner.
type ResumePrepareResultV1 struct {
	V        int                  `json:"v"`
	Prepared *PreparedResumeV1    `json:"prepared"`
	Mode     string               `json:"mode"`
	Primary  string               `json:"primary"`
	VM       *ResumeVMSelectionV1 `json:"vm,omitempty"`
}

// ResumeVMSelectionV1 describes the protected old selection. It supplies no
// runtime, policy, grant or native authority to the client or helper.
type ResumeVMSelectionV1 struct {
	V            int             `json:"v"`
	Need         json.RawMessage `json:"need"`
	PolicyMemory *string         `json:"policy_memory,omitempty"`
}

// ResumeVMPrepareArgsV1 completes only an actual retained cold preparation.
type ResumeVMPrepareArgsV1 struct {
	// RuntimeOrigin belongs only to this actual cold second phase.
	RuntimeOrigin json.RawMessage   `json:"runtime_origin_v1,omitempty"`
	V             int               `json:"v"`
	Prepared      *PreparedResumeV1 `json:"prepared"`
	Start         ResumeArgs        `json:"start"`
}

// Valid checks a descriptive reply shape. Its token still requires the same
// actual accepted-connection producer before any runtime action.
func (result *ResumePrepareResultV1) Valid() bool {
	return result != nil && result.V == 1 && result.Prepared.Valid() && (result.Mode == "cold" || result.Mode == "warm") && ValidSessionID(result.Primary) && (result.VM == nil || result.Mode == "cold" && result.VM.V == 1 && len(result.VM.Need) != 0)
}

// ClonePreparedResumeV1 owns a copy of the descriptive continuation handle.
func ClonePreparedResumeV1(p *PreparedResumeV1) *PreparedResumeV1 {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

// ResumePreparationMetadataV1 strips every typed private-value carrier before
// taking an owned wire snapshot. It grants no source or runtime permission.
func ResumePreparationMetadataV1(input ResumeArgs) (ResumeArgs, error) {
	input.Prepared, input.NamedSSHKeys, input.Secrets, input.RepositoryPolicy = nil, nil, nil, nil
	input.Members = nil
	input.SecretDelivery = nil
	raw, err := json.Marshal(input)
	if err != nil {
		return ResumeArgs{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ResumeArgs{}, err
	}
	// Private transport additions never become runtime selection metadata.
	delete(fields, "protected_trusted_secret_values")
	raw, err = json.Marshal(fields)
	if err != nil {
		return ResumeArgs{}, err
	}
	var output ResumeArgs
	if err := json.Unmarshal(raw, &output); err != nil {
		return ResumeArgs{}, err
	}
	return output, nil
}

// Format hides the continuation token in a preparation reply.
func (ResumePrepareResultV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }
