package api

// FeatureNetworkHolds advertises actual live held-request operations.
// Declaring this name alone does not advertise or activate a producer.
const FeatureNetworkHolds = "network-holds-v1"

// Public policy operations resolve only the current actor's opaque IDs.
const (
	OpPolicyPending  = "policy.pending"
	OpPolicyRefuse   = "policy.refuse"
	OpPolicyAnswer   = "policy.answer"
	OpPolicyResolve  = "policy.resolve"
	OpPolicyDescribe = "policy.describe-request"
)

// NetworkHoldScope is the requested answer scope, never a permission itself.
type NetworkHoldScope string

// Supported UI scopes omit all-sessions authority, as the policy sheet does.
const (
	NetworkHoldOnce    NetworkHoldScope = "once"
	NetworkHoldSession NetworkHoldScope = "session"
	NetworkHoldRepo    NetworkHoldScope = "repo"
	NetworkHoldProfile NetworkHoldScope = "profile"
)

// NetworkHoldQueryArgs selects a session's display snapshot.
type NetworkHoldQueryArgs struct {
	Version int    `json:"v"`
	Session string `json:"session"`
}

// NetworkHoldActionArgs asks the backend to resolve a live opaque request ID.
// It contains no display host or target. The backend checks current scope,
// deadline, source privacy, rules and capacity before performing any action.
type NetworkHoldActionArgs struct {
	Version int              `json:"v"`
	Session string           `json:"session"`
	Request string           `json:"request"`
	Scope   NetworkHoldScope `json:"scope,omitempty"`
	// Revision and Maximum are mandatory for positive answer/resolve. They
	// restrict the current private lookup; neither field grants a target.
	Revision uint64             `json:"revision,omitempty"`
	Maximum  NetworkHoldMaximum `json:"maximum,omitempty"`
}

// NetworkHoldAnswerResult contains opaque IDs and display scope only.
// A normal wider-scope resolution has a separate guarded backend result.
type NetworkHoldAnswerResult struct {
	Version    int              `json:"v"`
	Scope      NetworkHoldScope `json:"scope"`
	RuleIDs    []string         `json:"rule_ids,omitempty"`
	MemoryOnly bool             `json:"memory_only,omitempty"`
}
