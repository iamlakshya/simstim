package client

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// validateSourceSnapshot treats the host response as data. Only locally pinned
// identities select config sections. Original session values stay local;
// every host-reported network rule still passes H5 before any secret fetch.
func validateSourceSnapshot(sources []api.Layer, pin *api.PolicySourcePin) ([]api.Layer, error) {
	if pin == nil {
		return nil, policySourceStateError()
	}
	b, err := json.Marshal(sources)
	if err != nil || len(b)+1 > proto.MaxFrame {
		return nil, policySourceStateError()
	}
	_, clean, err := egress.MergeLayers(sources)
	if err != nil {
		return nil, policyError(err)
	}
	pins := map[api.LayerRef]bool{}
	for _, ref := range pin.Layers {
		pins[ref] = true
	}
	seen := map[api.LayerRef]bool{}
	for i, layer := range clean {
		ref := api.LayerRef{Kind: layer.Kind, Name: layer.Name, Source: layer.Source}
		if layer.Source == api.SourceRepositoryFile || layer.Kind == api.LayerBuiltin || layer.Kind == api.LayerLockdown {
			continue
		}
		if !pins[ref] {
			return nil, policySourceStateError()
		}
		seen[ref] = true
		if layer.Kind == api.LayerSession {
			clean[i].LayerValues = api.CloneLayerValues(pin.Session)
			clean[i].Preset = ""
			clean[i].Unknown = ""
		}
	}
	for ref := range pins {
		if !seen[ref] {
			return nil, policySourceStateError()
		}
	}
	return api.CloneLayers(clean), nil
}
