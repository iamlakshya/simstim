package api

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
	"time"
)

// Preset names one of the network presets of a sandbox (spec 7.2).
type Preset string

// Network presets.
const (
	PresetLocked   Preset = "locked"
	PresetBalanced Preset = "balanced"
	PresetOpen     Preset = "open"
)

// ParsePreset parses s as a Preset, or returns PresetBalanced when s is empty.
func ParsePreset(s string) (Preset, error) {
	switch Preset(strings.TrimSpace(s)) {
	case "":
		return PresetBalanced, nil
	case PresetLocked, PresetBalanced, PresetOpen:
		return Preset(strings.TrimSpace(s)), nil
	default:
		return "", fmt.Errorf("preset %q must be locked, balanced or open", s)
	}
}

// Unknown says what happens to a network flow that no rule covers (spec 7.2).
type Unknown string

// Actions for unknown destinations.
const (
	UnknownAsk      Unknown = "ask"
	UnknownDeny     Unknown = "deny"
	UnknownAllow    Unknown = "allow"
	UnknownClassify Unknown = "classify"
)

// ParseUnknown parses s as an Unknown action, or returns UnknownDeny when s is
// empty.
func ParseUnknown(s string) (Unknown, error) {
	trimmed := strings.TrimSpace(s)
	switch Unknown(trimmed) {
	case "":
		return UnknownDeny, nil
	case UnknownAsk, UnknownDeny, UnknownAllow, UnknownClassify:
		return Unknown(trimmed), nil
	default:
		return "", fmt.Errorf("unknown action %q must be ask, deny, allow or classify", s)
	}
}

// Door names a door of the wall (spec 7).
type Door string

// DoorNet is the outbound network door (spec 7.2).
const DoorNet Door = "net"

// LayerKind names one of the policy layers of spec 8.2, from broadest to
// narrowest, plus lockdown.
type LayerKind string

// Policy layer kinds.
const (
	LayerBuiltin  LayerKind = "builtin"
	LayerAll      LayerKind = "all"
	LayerHost     LayerKind = "host"
	LayerRepo     LayerKind = "repo"
	LayerProfile  LayerKind = "profile"
	LayerSession  LayerKind = "session"
	LayerLockdown LayerKind = "lockdown"
)

// Rule is one allow or deny rule on a door (spec 8.2).
type Rule struct {
	// Source distinguishes a repository file from a same-named laptop section.
	Source LayerSource `json:"source,omitempty"`

	ID      string    `json:"id,omitempty"`
	GrantID string    `json:"grant_id,omitempty" toml:"-"`
	Layer   LayerKind `json:"layer,omitempty"`
	Name    string    `json:"name,omitempty"`
	Allow   bool      `json:"allow"`
	Door    Door      `json:"door"`
	Target  string    `json:"target"`
	Mode    string    `json:"mode,omitempty"`
	// Methods preserves absent versus explicit empty exact HTTP token sets.
	Methods *[]string `json:"methods,omitempty" toml:"methods,omitempty"`
	Once    bool      `json:"once,omitempty"`
	Until   time.Time `json:"until,omitzero"`
}

// Lock holds values and denies that no narrower layer may override (spec 8.2).
type Lock struct {
	LayerValues

	Layer   LayerKind `json:"layer,omitempty"`
	Name    string    `json:"name,omitempty"`
	Preset  Preset    `json:"preset,omitempty"`
	Unknown Unknown   `json:"unknown,omitempty"`
	Deny    []string  `json:"deny,omitempty"`
}

// Layer is one layer of a session's policy before merge (spec 8.2).
type Layer struct {
	// ReviewedHash binds approved repository-file allowances to exact bytes.
	ReviewedHash string `json:"reviewed_hash,omitempty"`
	LayerValues
	// Source marks repository-file input, which may only narrow policy.
	Source LayerSource `json:"source,omitempty"`

	Kind    LayerKind `json:"kind"`
	Name    string    `json:"name,omitempty"`
	Preset  Preset    `json:"preset,omitempty"`
	Unknown Unknown   `json:"unknown,omitempty"`
	Rules   []Rule    `json:"rules,omitempty"`
	Lock    *Lock     `json:"lock,omitempty"`
}

// LayerSource distinguishes trusted laptop layers from repository files.
type LayerSource string

// SourceRepositoryFile is an untrusted repository policy file.
const SourceRepositoryFile LayerSource = "repository-file"

// LayerRef identifies a layer without colliding with a same-named file.
type LayerRef struct {
	Source LayerSource `json:"source,omitempty"`
	Kind   LayerKind   `json:"kind"`
	Name   string      `json:"name,omitempty"`
}

// Policy is the merged policy of a sandbox.
type Policy struct {
	// Sources is a read-only policy.ls snapshot. It is never persisted in Policy.
	Sources []Layer `json:"sources,omitempty"`
	// Order is complete and unique when present. Older policies omit it.
	Order []LayerRef `json:"order,omitempty"`
	LayerValues
	// Origins names the source of each merged setting.
	Origins []ValueOrigin `json:"origins,omitempty"`

	Preset  Preset  `json:"preset"`
	Unknown Unknown `json:"unknown,omitempty"`
	Rules   []Rule  `json:"rules,omitempty"`
	Locks   []Lock  `json:"locks,omitempty"`
}

// RuleID returns the deterministic ID of r: "g" followed by the first 6
// lower-case base32 characters of SHA-256 over its layer kind, layer name,
// allow or deny, door, target and mode, one on each line (spec 8.2).
func RuleID(r Rule) string {
	act := "deny"
	if r.Allow {
		act = "allow"
	}
	door := r.Door
	if door == "" {
		door = DoorNet
	}
	identity := strings.Join([]string{
		string(r.Layer), r.Name, act, string(door), r.Target, r.Mode,
	}, "\n") + "\n"
	if r.Methods != nil {
		methods, err := CanonicalHTTPMethods(r.Methods)
		if err != nil {
			// RuleID remains total. Admission rejects this invalid set before
			// the identity can enter policy or the once-grant ledger.
			methods = r.Methods
		}
		identity += "\x00policy-http-methods-v1\n" + strings.Join(*methods, "\n") + "\n"
	}
	sum := sha256.Sum256([]byte(identity))
	enc := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
	return "g" + enc[:6]
}

// PolicyAddArgs asks a supervisor to add a rule to the session layer.
type PolicyAddArgs struct {
	ID     string `json:"id,omitempty"`
	Allow  bool   `json:"allow"`
	Door   Door   `json:"door,omitempty"`
	Target string `json:"target"`
	Mode   string `json:"mode,omitempty"`
	// Methods preserves absent versus explicit empty exact HTTP token sets.
	Methods *[]string `json:"methods,omitempty" toml:"methods,omitempty"`
	Once    bool      `json:"once,omitempty"`
	Until   time.Time `json:"until,omitzero"`
}

// PolicyRmArgs asks a supervisor to remove a rule from the session layer.
type PolicyRmArgs struct {
	Session string `json:"session,omitempty"`
	ID      string `json:"id,omitempty"`
	Door    Door   `json:"door,omitempty"`
	Target  string `json:"target,omitempty"`
}

// PolicyLogArgs asks a supervisor for door decisions from audit.jsonl.
type PolicyLogArgs struct {
	ID     string `json:"id,omitempty"`
	Since  string `json:"since,omitempty"`
	Door   Door   `json:"door,omitempty"`
	By     string `json:"by,omitempty"`
	Max    int    `json:"max,omitempty"`
	Page   bool   `json:"page,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
