package egress

import (
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	CtlCapabilities    = "capabilities"
	CtlMemberAdd       = "member.add"
	MemberAddV1Feature = "member-add-v1"
	MemberRegistryV1   = 1
)

// CapabilitiesResult advertises internal control operations, separately from
// the public CA-only InitResult. A caller checks this before member.add or a
// saved multi-agent initialisation. An old helper fails closed on the query.
type CapabilitiesResult struct {
	Features []string `json:"features"`
}

// MemberAddArgs is sent only by the trusted supervisor after saving a child
// reservation. H5 approves every value against the original parent policy.
// V1 explicitly carries legacy exposed values. Bound forms need a separately
// negotiated version of this same registry and are refused by strict decoding.
type MemberAddArgs struct {
	Version int `json:"version"`
	// Kind empty means a custom command adds no builtin. Every nonempty
	// kind must be a canonical trusted agent-table row.
	Kind          string                `json:"kind"`
	ExposedValues map[string]api.Secret `json:"exposed_values,omitempty"`
}

// MemberAddResult contains only the joining member's requested environment.
// It never changes the environment of an existing member.
type MemberAddResult struct {
	Version int                   `json:"version"`
	Env     map[string]api.Secret `json:"env"`
}

// MemberAudit contains public names only. It carries no value or binding data.
type MemberAudit struct {
	Kind    string   `json:"kind"`
	Secrets []string `json:"secrets"`
}

func (InitArgs) Format(f fmt.State, _ rune)        { _, _ = io.WriteString(f, api.Redacted) }
func (MemberAddArgs) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, api.Redacted) }
func (MemberAddResult) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
