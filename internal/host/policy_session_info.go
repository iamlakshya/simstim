package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// policySessionInfo is bounded display data from the actual protected record.
// It creates no readiness, original Need, source or secret-delivery authority.
func policySessionInfo(m *Manifest, info *SessionInfo) {
	if m == nil || info == nil || m.Wall == nil || m.SandboxOf != "" || len(m.Layers) == 0 {
		return
	}
	desired, err := api.DesiredPolicyLayersV1(m.Layers, m.PolicyNext)
	if err != nil {
		return
	}
	hash, err := api.HashPolicyLaptopLayersV1(desired)
	if err != nil {
		return
	}
	info.LayersHash = hash
	if len(m.PolicyNext) == 0 {
		return
	}
	_, keys, err := egress.ProjectLivePolicyLayersV1(m.Layers, desired)
	if err != nil {
		return
	}
	info.PolicyNextStart = keys
	policy, _, err := egress.MergeLayers(desired)
	if err != nil {
		return
	}
	for _, key := range keys {
		if key == "sandbox.wall" && policy.Sandbox.Wall != nil {
			info.PolicyNextWall = *policy.Sandbox.Wall
		}
	}
}
