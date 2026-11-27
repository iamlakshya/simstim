package api

// ProjectClientImagePolicy clones outgoing layers and omits the laptop's
// Sigstore root path. The original local layers and source pins keep it.
// This projection grants no image, signature or native boot authority.
func ProjectClientImagePolicy(layers []Layer) []Layer {
	out := CloneLayers(layers)
	for i := range out {
		if policy := out[i].Sandbox.ImagePolicy; policy != nil {
			policy.TrustedRoot = nil
		}
		if lock := out[i].Lock; lock != nil && lock.Sandbox.ImagePolicy != nil {
			lock.Sandbox.ImagePolicy.TrustedRoot = nil
		}
	}
	return out
}
