package api

// FeaturePolicyLogCursor offers bounded pages on the existing policy.log op.
const FeaturePolicyLogCursor = "policy-log-cursor-v1"

// Policy log page limits bound each reply independently of log history.
const (
	PolicyLogDefaultLimit   = 128
	PolicyLogMaxLimit       = 256
	PolicyLogMaxDataBytes   = 1 << 20
	PolicyLogMaxCursorBytes = 2 << 10
)

// NetAuditEntry is one pre-scrubbed network audit record, as spec7.2 defines.
// Secret holds only a public name, never a secret value.
type NetAuditEntry struct {
	V        int    `json:"v"`
	T        string `json:"t"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Mode     string `json:"mode"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
	Rule     string `json:"rule"`
	Secret   string `json:"secret"`
	Decision string `json:"decision"`
	// By is finite trusted decision provenance, never parsed from Rule.
	By     string `json:"by,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Usage marks the one logical answer's reported or byte-estimated counts.
	Usage          string              `json:"usage,omitempty"`
	ModelUsage     *ModelUsageCountsV1 `json:"model_usage,omitempty"`
	UsageSaturated bool                `json:"usage_saturated,omitempty"`
	// UnknownDestination is an actor-local opaque bookkeeping ID. It is
	// never a destination, permission or reusable cross-actor receipt.
	UnknownDestination uint64 `json:"unknown_destination,omitempty"`
}

// PolicyLogPage is one bounded read from a trusted append-only audit log.
// Cursor is opaque progress data. Equal entries remain separate records.
type PolicyLogPage struct {
	Entries []NetAuditEntry `json:"entries"`
	Cursor  string          `json:"cursor"`
	More    bool            `json:"more"`
}
