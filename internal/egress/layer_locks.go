package egress

import (
	"reflect"
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type lockedValue struct {
	value reflect.Value
	lock  api.Lock
	at    int
}

func resolveLayerValues(layers []api.Layer) (api.LayerValues, []api.ValueOrigin, []api.Lock, error) {
	fixed := map[string]lockedValue{}
	var locks []api.Lock
	for i, l := range layers {
		if l.Lock == nil {
			continue
		}
		if l.Kind != api.LayerAll && l.Kind != api.LayerHost {
			return api.LayerValues{}, nil, nil, &proto.Error{Code: "layer_key", Message: "only all sessions and host layers may hold locks", Hint: "Put the lock in config.toml or a host entry."}
		}
		lk := *l.Lock
		lk.Layer, lk.Name = l.Kind, l.Name
		var scoped []api.NetworkRule
		for _, raw := range lk.Network.Rules {
			if raw.Allow || raw.Remove {
				return api.LayerValues{}, nil, nil, badLayerValue("lock.network.rules", "holds denies only; removals are not locks")
			}
			clean, err := CanonicalNetworkRule(raw)
			if err != nil {
				return api.LayerValues{}, nil, nil, err
			}
			scoped = append(scoped, clean)
		}
		proxy := api.Layer{LayerValues: lk.LayerValues, Preset: lk.Preset, Unknown: lk.Unknown}
		proxy.Network.Rules = nil
		if err := canonicalLayerValues(&proxy); err != nil {
			return api.LayerValues{}, nil, nil, err
		}
		if len(proxy.Network.Allow) > 0 {
			return api.LayerValues{}, nil, nil, &proto.Error{Code: "layer_key", Message: "a lock may hold denies, not allows", Hint: "Move network.allow outside [lock]."}
		}
		lk.LayerValues, lk.Preset, lk.Unknown = proxy.LayerValues, proxy.Preset, proxy.Unknown
		lk.Network.Rules = scoped
		lk.Deny = append(slices.Clone(lk.Deny), proxy.Network.Deny...)
		for n, target := range lk.Deny {
			clean, err := ValidateRuleTarget(target)
			if err != nil {
				return api.LayerValues{}, nil, nil, err
			}
			lk.Deny[n] = clean
		}
		sort.Strings(lk.Deny)
		lk.Deny = slices.Compact(lk.Deny)
		for key, value := range valuesMap(lk.LayerValues) {
			if key == "network.deny" || key == "network.rules" {
				continue
			}
			if old, ok := fixed[key]; ok && !reflect.DeepEqual(old.value.Interface(), value.Interface()) {
				return api.LayerValues{}, nil, nil, layerError(l, key, old.lock)
			}
			fixed[key] = lockedValue{value: value, lock: lk, at: i}
		}
		locks = append(locks, lk)
	}
	var merged api.LayerValues
	origins := map[string]api.ValueOrigin{}
	var decoysOff *api.Layer
	for i, l := range layers {
		for key, value := range valuesMap(l.LayerValues) {
			if lock, ok := fixed[key]; ok && i > lock.at && !reflect.DeepEqual(lock.value.Interface(), value.Interface()) {
				return api.LayerValues{}, nil, nil, layerError(l, key, lock.lock)
			}
		}
		if l.Source == api.SourceRepositoryFile {
			if err := repositoryNarrows(merged, l); err != nil {
				return api.LayerValues{}, nil, nil, err
			}
		}
		if decoysOff == nil && l.Sandbox.Decoys != nil && !*l.Sandbox.Decoys {
			copy := l
			decoysOff = &copy
		}
		mergeLayerValues(&merged, l, origins)
	}
	// The global opt-out cannot silently override a final true fixed lock,
	// including an earlier disabling source. Reject the complete policy.
	if decoysOff != nil {
		if lock, ok := fixed["sandbox.decoys"]; ok && lock.value.Elem().Bool() {
			return api.LayerValues{}, nil, nil, layerError(*decoysOff, "sandbox.decoys", lock.lock)
		}
	}
	_ = visitValues(reflect.ValueOf(&merged).Elem(), "", func(key string, dst reflect.Value) error {
		if lock, ok := fixed[key]; ok {
			dst.Set(cloneValue(lock.value))
			origins[key] = api.ValueOrigin{Key: key, Layer: lock.lock.Layer, Name: lock.lock.Name}
		}
		return nil
	})
	keys := make([]string, 0, len(origins))
	for key := range origins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]api.ValueOrigin, 0, len(keys))
	for _, key := range keys {
		out = append(out, origins[key])
	}
	return merged, out, locks, nil
}

func repositoryNarrows(old api.LayerValues, l api.Layer) error {
	for key := range valuesMap(l.LayerValues) {
		switch key {
		case "network.deny", "network.rules", "sandbox.wall", "sandbox.preset", "network.unknown":
		default:
			return &proto.Error{Code: "layer_key", Message: "the repository file cannot set " + key, Hint: "Move this setting into a trusted repository section in config.toml."}
		}
	}
	if p := l.Sandbox.Preset; p != nil {
		rank := map[string]int{"locked": 0, "balanced": 1, "open": 2}
		before := "balanced"
		if old.Sandbox.Preset != nil {
			before = *old.Sandbox.Preset
		}
		if rank[*p] > rank[before] {
			return badLayerValue("sandbox.preset", "in a repository file may only tighten the preset")
		}
	}
	if u := l.Network.Unknown; u != nil {
		before := "ask"
		if old.Network.Unknown != nil {
			before = *old.Network.Unknown
		}
		if *u != before && *u != "deny" && before != "allow" {
			return badLayerValue("network.unknown", "in a repository file may only narrow the unknown action")
		}
	}
	return nil
}
