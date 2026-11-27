package client

import (
	"context"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// Only the original protected LOCAL primary chooses this otherwise empty
// exact source. A host directory and secondary seeds cannot supply it.
// The empty layer grants nothing; it retains the exact identity for a later
// local repository edit and the existing immutable-source refresh path.
func (p *newInputPreparation) retainPrimaryRepositorySourceV1(ctx context.Context) error {
	if p == nil || p.closed || p.operation == nil || p.discovery == nil {
		return policySourceStateError()
	}
	request := grantRequestOf(ctx)
	if request == nil {
		return grantOperationError()
	}
	request.mu.Lock()
	original := request.newInputs == p
	request.mu.Unlock()
	if !original {
		return policySourceStateError()
	}
	primary := p.discovery.primary()
	if primary == nil || primary.origin == "" {
		return nil
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	// primary.origin is the retained raw Git metadata. Do not normalise a
	// section name or a previously normalised origin a second time.
	origin, err := NormaliseOrigin(primary.origin)
	if err != nil || strings.Contains(origin, "*") {
		return badOrigin()
	}
	p.layers, err = withExactPrimaryRepositorySourceV1(p.layers, origin)
	return err
}

func withExactPrimaryRepositorySourceV1(layers []api.Layer, origin string) ([]api.Layer, error) {
	if origin == "" || strings.Contains(origin, "*") {
		return nil, policySourceStateError()
	}
	for _, layer := range layers {
		if layer.Kind == api.LayerRepo && layer.Source != api.SourceRepositoryFile && layer.Name == origin {
			return api.CloneLayers(layers), nil
		}
	}
	at := -1
	for index, layer := range layers {
		if layer.Kind == api.LayerProfile || layer.Kind == api.LayerSession {
			at = index
			break
		}
	}
	if at < 0 {
		return nil, policySourceStateError()
	}
	result := api.CloneLayers(layers)
	result = append(result, api.Layer{})
	copy(result[at+1:], result[at:])
	result[at] = api.Layer{Kind: api.LayerRepo, Name: origin}
	return result, nil
}
