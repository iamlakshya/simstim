package api

import "encoding/json"

// SandboxCohortInfoArgsV1 extends the protected live info hop. Its expected
// tuples restrict a fresh private actor read; they issue no value permission.
type SandboxCohortInfoArgsV1 struct {
	Cohort           bool                           `json:"cohort,omitempty"`
	TrustedReadiness *TrustedSecretReadinessQueryV1 `json:"trusted_readiness,omitempty"`
}

// SandboxCohortInfoResultV1 is transient control DATA. Current is the same
// protected live manifest read; readiness must never be saved in a manifest.
type SandboxCohortInfoResultV1 struct {
	Current          json.RawMessage           `json:"current"`
	TrustedReadiness *TrustedSecretReadinessV1 `json:"trusted_readiness"`
}
