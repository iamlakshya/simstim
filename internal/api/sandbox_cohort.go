package api

import (
	"encoding/json"
	"fmt"

	"github.com/iamlakshya/simstim/internal/proto"
)

// FeatureSandboxCohort gates the additive sandbox.snapshot cohort envelope.
const FeatureSandboxCohort = "sandbox-cohort-v1"

// FeatureSandboxReviewContext guards saved applied context for stopped review.
const FeatureSandboxReviewContext = "sandbox-review-context-v1"

// SandboxReviewContextOp reads only saved protected publication context.
const SandboxReviewContextOp = "sandbox.review-context-v1"

// SandboxCohortView is a restriction-only view read under one primary lease.
// None of its fields grants names, fetches, exposure, bindings or source pins.
type SandboxCohortView struct {
	Snapshot      *SandboxSnapshot    `json:"snapshot"`
	SecretState   *SessionSecretState `json:"secret_state"`
	RunGeneration string              `json:"run_generation"`
}

// Clone owns the unchanged canonical snapshot and public-state DTOs.
func (v *SandboxCohortView) Clone() *SandboxCohortView {
	if v == nil {
		return nil
	}
	return &SandboxCohortView{Snapshot: v.Snapshot.Clone(), SecretState: CloneSessionSecretState(v.SecretState), RunGeneration: v.RunGeneration}
}

// DecodeSandboxCohort applies the existing strict DTO codecs. Complete public
// semantic validation remains the canonical egress validator's responsibility.
func DecodeSandboxCohort(raw []byte, original string) (*SandboxCohortView, error) {
	fields, err := JSONObjectFields(raw, "snapshot", "secret_state", "run_generation")
	if err != nil || len(fields) != 3 || len(raw)+1 > proto.MaxFrame {
		return nil, fmt.Errorf("invalid sandbox cohort envelope")
	}
	snapshot, err := DecodeSandboxSnapshot(fields["snapshot"], original, proto.MaxFrame)
	if err != nil {
		return nil, err
	}
	var view SandboxCohortView
	if json.Unmarshal(raw, &view) != nil || view.SecretState == nil || !view.SecretState.Supported() || view.SecretState.Generation == "" || view.RunGeneration == "" {
		return nil, fmt.Errorf("unsupported or incomplete current sandbox cohort")
	}
	view.Snapshot = snapshot
	return view.Clone(), nil
}
