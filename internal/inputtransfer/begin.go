package inputtransfer

import "github.com/iamlakshya/simstim/internal/api"

// BeginArgs uses the existing authenticated dedicated input continuation.
// Initial/wake requires nil Summary and the protected New/Resume declaration.
// Pending running copies alone supply a fresh copy-only summary for durable
// CAS under that exact current run before any put stream is accepted.
type BeginArgs struct {
	Scope
	Summary *api.InputSummary `json:"summary,omitempty"`
	// FinishOnly recovers this exact prior locally selected operation. It
	// never opens a payload stream or selects laptop files from host claims.
	FinishOnly    bool   `json:"finish_only,omitempty"`
	RunGeneration string `json:"run_generation,omitempty"`
}

// BeginResult binds a continuation to the actual current supervisor run.
// Finished is true only for an exact idempotent finish-only recovery.
type BeginResult struct {
	Version       int    `json:"version"`
	RunGeneration string `json:"run_generation"`
	Finished      bool   `json:"finished,omitempty"`
}

// CtlStatus queries the existing Init input operation without performing writes.
const CtlStatus = "inputs.status-v1"

// StatusResult describes only the matching already-owned guest operation.
// A host may use it solely with its own canonical sender EOF receipt.
type StatusResult struct {
	Version int `json:"version"`
	Scope
	Found    bool `json:"found"`
	Joined   bool `json:"joined"`
	Complete bool `json:"complete"`
}

// FinishFeature guards exact same-operation Describe and Finish-only recovery.
const FinishFeature = "session-input-finish-v1"

// Describe names only an original locally retained current-run operation.
const Describe = "inputs.describe-v1"

// OperationArgs selects no laptop source, payload path or new input operation.
type OperationArgs struct {
	Scope
	RunGeneration string `json:"run_generation"`
}

// DescribeResult contains the original protected summary as restriction only.
// The client must compare its original LOCAL canonical full-summary hash.
type DescribeResult struct {
	Version       int              `json:"version"`
	RunGeneration string           `json:"run_generation"`
	Summary       api.InputSummary `json:"summary"`
}
