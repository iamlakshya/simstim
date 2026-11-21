package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// PolicyLaptopLayersV1 returns the ordered owned laptop-source vector. It
// excludes built-ins, repository files, session grants and a lockdown.
func PolicyLaptopLayersV1(layers []Layer) ([]Layer, error) {
	selected := make([]Layer, 0)
	seen := map[LayerRef]bool{}
	kinds := map[LayerKind]bool{}
	for _, layer := range layers {
		ref := LayerRef{Kind: layer.Kind, Name: layer.Name, Source: layer.Source}
		if !ValidLayerRef(ref) {
			return nil, ErrPolicyLiveV1
		}
		if layer.Source == SourceRepositoryFile || layer.Kind == LayerBuiltin || layer.Kind == LayerSession || layer.Kind == LayerLockdown {
			continue
		}
		if !ValidSelectedLayerRef(ref) || seen[ref] || kinds[ref.Kind] && ref.Kind != LayerRepo || layer.ReviewedHash != "" {
			return nil, ErrPolicyLiveV1
		}
		seen[ref], kinds[ref.Kind] = true, true
		selected = append(selected, layer)
	}
	return CloneLayers(selected), nil
}

// HashPolicyLaptopLayersV1 hashes precisely the JSON source vector that the
// replacement carries. Session-only changes never alter this hash.
func HashPolicyLaptopLayersV1(layers []Layer) (string, error) {
	selected, err := PolicyLaptopLayersV1(layers)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return "", fmt.Errorf("encode live policy sources: %w. Try: check the selected policy layers", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ReplacePolicyLaptopLayersV1 preserves every non-laptop layer and the exact
// original selected identities and order. Host-supplied names cannot add one.
func ReplacePolicyLaptopLayersV1(current, updates []Layer) ([]Layer, error) {
	selected, err := PolicyLaptopLayersV1(current)
	if err != nil {
		return nil, err
	}
	incoming, err := PolicyLaptopLayersV1(updates)
	if err != nil || len(incoming) != len(updates) || len(incoming) != len(selected) || len(incoming) == 0 {
		return nil, ErrPolicyLiveV1
	}
	for index, layer := range selected {
		if layer.Kind != incoming[index].Kind || layer.Name != incoming[index].Name {
			return nil, ErrPolicyLiveV1
		}
	}
	next := CloneLayers(current)
	index := 0
	for at, layer := range next {
		if layer.Source == "" && (layer.Kind == LayerAll || layer.Kind == LayerHost || layer.Kind == LayerRepo || layer.Kind == LayerProfile) {
			next[at] = incoming[index]
			index++
		}
	}
	return next, nil
}
