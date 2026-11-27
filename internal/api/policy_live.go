package api

import "errors"

// FeaturePolicyLiveV1 requires the actual hash-CAS replacement producer.
const FeaturePolicyLiveV1 = "policy-live-v1"

// PolicyReplaceVersionV1 is the closed live laptop-layer replacement schema.
const PolicyReplaceVersionV1 = 1

// PolicyReplaceArgsV1 replaces only the original selected laptop layers.
// The host preserves its built-in, repository-file, session and lockdown data.
// A layer DTO never supplies native boot or secret-delivery authority.
type PolicyReplaceArgsV1 struct {
	Version      int     `json:"v"`
	Session      string  `json:"session"`
	PreviousHash string  `json:"previous_hash"`
	Layers       []Layer `json:"layers"`
}

// PolicyReplaceResultV1 acknowledges the actual live swap and saved sources.
// A conflict supplies only the current hash; it acknowledges no replacement.
type PolicyReplaceResultV1 struct {
	Version   int      `json:"v"`
	Hash      string   `json:"hash"`
	State     string   `json:"state"`
	NextStart []string `json:"next_start,omitempty"`
}

// ErrPolicyLiveV1 marks invalid or unsupported replacement data.
var ErrPolicyLiveV1 = errors.New("invalid live policy replacement. Try: refresh the policy with matching client and host helpers")

// ValidPolicyHashV1 checks the exact SHA-256 encoding, not an approval token.
func ValidPolicyHashV1(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, value := range hash {
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}

// Validate checks only the closed typed request. Actual selected identities,
// current hash, Source, locks and live egress lifetime remain with their owners.
func (args PolicyReplaceArgsV1) Validate() error {
	if args.Version != PolicyReplaceVersionV1 || !ValidSessionID(args.Session) || !ValidPolicyHashV1(args.PreviousHash) || len(args.Layers) == 0 {
		return ErrPolicyLiveV1
	}
	laptop, err := PolicyLaptopLayersV1(args.Layers)
	if err != nil || len(laptop) != len(args.Layers) {
		return ErrPolicyLiveV1
	}
	return nil
}

// Validate rejects unknown dispositions and descriptive next-start fields.
func (result PolicyReplaceResultV1) Validate() error {
	if result.Version != PolicyReplaceVersionV1 || !ValidPolicyHashV1(result.Hash) || result.State != "applied" && result.State != "conflict" && result.State != "unchanged" || result.State == "conflict" && len(result.NextStart) != 0 {
		return ErrPolicyLiveV1
	}
	known := map[string]bool{"rules": true, "locks": true}
	for _, name := range ExactJSONFieldNames(SandboxValues{}) {
		if name != "preset" && name != "push" {
			known["sandbox."+name] = true
		}
	}
	previous := ""
	for _, name := range result.NextStart {
		if !known[name] || name <= previous {
			return ErrPolicyLiveV1
		}
		previous = name
	}
	return nil
}
