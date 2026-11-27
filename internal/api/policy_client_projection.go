package api

// ProjectClientInputSources returns the outbound typed policy projection of
// the SAME original local snapshot. Only laptop copy-source expressions are
// removed. The caller retains the original for its local creation pin.
// Original source and proposal hash fields are preserved, never relabelled.
func ProjectClientInputSources(original []Layer) []Layer {
	outbound := CloneLayers(original)
	for i := range outbound {
		outbound[i].Sandbox.CopyIn = nil
		if outbound[i].Lock != nil {
			outbound[i].Lock.Sandbox.CopyIn = nil
		}
	}
	return outbound
}

// HasClientInputSources reports laptop source expressions which must not be
// accepted in an outbound typed policy. It examines every layer and lock.
func HasClientInputSources(layers []Layer) bool {
	for _, layer := range layers {
		if len(layer.Sandbox.CopyIn) != 0 || layer.Lock != nil && len(layer.Lock.Sandbox.CopyIn) != 0 {
			return true
		}
	}
	return false
}
