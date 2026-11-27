package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// PolicySnapshot supplies read-only canonical source layers for a next-start
// preview. The caller supplies an owned api.CloneLayers snapshot, taken under
// its state lock. Sources is attached only to the response, never stored.
func PolicySnapshot(layers []api.Layer) (api.Policy, error) {
	return PolicySnapshotWithNext(layers, nil)
}

// PolicySnapshotWithNext reports applied values and a separate pending view.
// Unsupported pending capsules stay opaque in the manifest and refuse this
// projection; a read cannot clear or reinterpret them.
func PolicySnapshotWithNext(layers []api.Layer, pending json.RawMessage) (api.Policy, error) {
	policy, clean, err := egress.MergeLayers(layers)
	if err != nil {
		return api.Policy{}, err
	}
	policy.Sources = clean
	desired, err := api.DesiredPolicyLayersV1(clean, pending)
	if err != nil {
		return api.Policy{}, err
	}
	policy.LayersHash, err = api.HashPolicyLaptopLayersV1(desired)
	if err != nil {
		return api.Policy{}, err
	}
	if len(pending) != 0 {
		_, policy.NextStart, err = egress.ProjectLivePolicyLayersV1(clean, desired)
		if err != nil {
			return api.Policy{}, err
		}
		policy.NextLayers, err = api.PolicyLaptopLayersV1(desired)
		if err != nil {
			return api.Policy{}, err
		}
	}
	return policy, nil
}
