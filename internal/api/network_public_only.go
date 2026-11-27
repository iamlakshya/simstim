package api

// FeatureNetworkPublicOnly prevents an older helper from ignoring an address
// restriction in a rule, saved layer or opaque held-request answer.
const FeatureNetworkPublicOnly = "network-public-only-v1"

// PolicyUsesPublicOnly reports a restriction requirement, never permission.
func PolicyUsesPublicOnly(policy Policy) bool {
	for _, rule := range policy.Rules {
		if rule.PublicOnly {
			return true
		}
	}
	if layerValuesUsePublicOnly(policy.LayerValues) || LayersUsePublicOnly(policy.Sources) {
		return true
	}
	for _, lock := range policy.Locks {
		if layerValuesUsePublicOnly(lock.LayerValues) {
			return true
		}
	}
	return false
}

// LayersUsePublicOnly finds restrictions before any older reader sees layers.
func LayersUsePublicOnly(layers []Layer) bool {
	for _, layer := range layers {
		if layerValuesUsePublicOnly(layer.LayerValues) {
			return true
		}
		for _, rule := range layer.Rules {
			if rule.PublicOnly {
				return true
			}
		}
		if layer.Lock != nil && layerValuesUsePublicOnly(layer.Lock.LayerValues) {
			return true
		}
	}
	return false
}

func layerValuesUsePublicOnly(values LayerValues) bool {
	for _, rule := range values.Network.Rules {
		if rule.PublicOnly {
			return true
		}
	}
	return false
}
