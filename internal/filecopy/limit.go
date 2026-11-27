package filecopy

import "github.com/iamlakshya/simstim/internal/api"

// CopyMax consumes the actual resolved policy through the canonical size
// grammar. Missing policy is unknown; an absent cp_max in known policy uses
// the spec's 256 MiB default. Explicit invalid/empty/zero is never clamped.
func CopyMax(policy *api.Policy) (int64, error) {
	if policy == nil {
		return 0, failure("cp_policy", "the session's resolved copy policy is unavailable")
	}
	if policy.Sandbox.CPMax == nil {
		return 256 << 20, nil
	}
	size, err := api.ParseSize(*policy.Sandbox.CPMax)
	if err != nil || size <= 0 {
		return 0, &Error{Code: "cp_policy", Message: "sandbox.cp_max is not a positive size", Hint: "Set sandbox.cp_max to a positive size such as 256M in the original policy."}
	}
	return size, nil
}
