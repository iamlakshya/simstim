package egress

import (
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// ProjectLivePolicyLayersV1 keeps the actual applied source vector safe for
// existing restart, ordinary mutation and lease consumers. Pending nonlive
// laptop values belong only in their separate opaque capsule.
func ProjectLivePolicyLayersV1(applied, desired []api.Layer) ([]api.Layer, []string, error) {
	if len(applied) != len(desired) {
		return nil, nil, api.ErrPolicyLiveV1
	}
	next := api.CloneLayers(desired)
	previous := api.CloneLayers(applied)
	changed := map[string]bool{}
	for index, current := range previous {
		candidate := next[index]
		if current.Kind != candidate.Kind || current.Name != candidate.Name || current.Source != candidate.Source {
			return nil, nil, api.ErrPolicyLiveV1
		}
		if current.Source != "" || current.Kind == api.LayerBuiltin || current.Kind == api.LayerSession || current.Kind == api.LayerLockdown {
			if !reflect.DeepEqual(current, candidate) {
				return nil, nil, api.ErrPolicyLiveV1
			}
			continue
		}
		for _, key := range nonliveSandboxChangesV1(current.Sandbox, candidate.Sandbox) {
			changed[key] = true
		}
		sandbox := current.Sandbox
		sandbox.Preset, sandbox.Push = candidate.Sandbox.Preset, candidate.Sandbox.Push
		next[index].Sandbox = sandbox
		next[index].Rules = append(nonliveRulesV1(current.Rules), liveRulesV1(candidate.Rules)...)
		if !reflect.DeepEqual(nonliveRulesV1(current.Rules), nonliveRulesV1(candidate.Rules)) {
			changed["rules"] = true
		}
		projected := projectSourceLockV1(current.Lock, candidate.Lock, current)
		if !reflect.DeepEqual(projected, candidate.Lock) {
			changed["locks"] = true
		}
		next[index].Lock = projected
	}
	if _, _, err := MergeLayers(api.CloneLayers(next)); err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(changed))
	for key := range changed {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return api.CloneLayers(next), keys, nil
}

func projectSourceLockV1(applied, desired *api.Lock, layer api.Layer) *api.Lock {
	var old, proposal []api.Lock
	if applied != nil {
		copy := *applied
		copy.Layer, copy.Name = layer.Kind, layer.Name
		old = []api.Lock{copy}
	}
	if desired != nil {
		copy := *desired
		copy.Layer, copy.Name = layer.Kind, layer.Name
		proposal = []api.Lock{copy}
	}
	projected := projectLiveLocksV1(old, proposal)
	if len(projected) == 0 {
		return nil
	}
	copy := projected[0]
	// A source lock carries the original source representation; the merger
	// assigns its layer and name, as it does for normal laptop config.
	if desired != nil {
		copy.Layer, copy.Name = desired.Layer, desired.Name
	} else if applied != nil {
		copy.Layer, copy.Name = applied.Layer, applied.Name
	}
	return &copy
}
