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
	// TrustedReadiness is transient live DATA and is never saved or a fetch grant.
	TrustedReadiness *TrustedSecretReadinessV1 `json:"trusted_readiness,omitempty"`
	Snapshot         *SandboxSnapshot          `json:"snapshot"`
	SecretState      *SessionSecretState       `json:"secret_state"`
	RunGeneration    string                    `json:"run_generation"`
}

// Clone owns the unchanged canonical snapshot and public-state DTOs.
func (v *SandboxCohortView) Clone() *SandboxCohortView {
	if v == nil {
		return nil
	}
	return &SandboxCohortView{TrustedReadiness: CloneTrustedSecretReadinessV1(v.TrustedReadiness), Snapshot: v.Snapshot.Clone(), SecretState: CloneSessionSecretState(v.SecretState), RunGeneration: v.RunGeneration}
}

// DecodeSandboxCohort applies the existing strict DTO codecs. Complete public
// semantic validation remains the canonical egress validator's responsibility.
func DecodeSandboxCohort(raw []byte, original string) (*SandboxCohortView, error) {
	fields, err := JSONObjectFields(raw, "snapshot", "secret_state", "run_generation", "trusted_readiness")
	if err != nil || (len(fields) != 3 && len(fields) != 4) || len(fields) == 4 && fields["trusted_readiness"] == nil || len(raw)+1 > proto.MaxFrame {
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
	if view.TrustedReadiness != nil {
		if !ExactJSONFields(fields["trusted_readiness"], (*TrustedSecretReadinessV1)(nil)) || !validCohortReadinessV1(view.TrustedReadiness, view.SecretState.Generation) {
			return nil, fmt.Errorf("invalid transient trusted credential observation")
		}
	}
	view.Snapshot = snapshot
	return view.Clone(), nil
}

// This validates DATA shape only; the actual original caller additionally
// checks ValidFor against its own locally captured expected role tuples.
func validCohortReadinessV1(value *TrustedSecretReadinessV1, generation string) bool {
	if value == nil || value.Version != 1 || value.Generation != generation || value.Unarmed == nil {
		return false
	}
	previous := ""
	for _, name := range value.Unarmed {
		if CheckSecretName(name, false) != SecretNameOK || name <= previous {
			return false
		}
		previous = name
	}
	return true
}
