package client

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

// This optional data can only remove duplicate local accounting. An unusable
// sample stays absent; it cannot repair Room or the enclosing known facts.
func decodeHostMemoryObservationV1(ctx context.Context, raw []byte, facts *api.HostFacts) (*api.MemoryObservationV1, error) {
	discard := func() (*api.MemoryObservationV1, error) { return nil, ctx.Err() }
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > api.MaxMemoryObservationBytesV1 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return discard()
	}
	projection, err := api.NewJSONBytesProjection(ctx, raw)
	if err != nil {
		return discard()
	}
	root, err := projection.Root()
	if err != nil {
		return discard()
	}
	names := api.ExactJSONFieldNames(api.MemoryObservationV1{})
	seen := make([]bool, len(names))
	var charges api.JSONSpan
	found := false
	err = projection.Object(root, names, func(field api.JSONField) error {
		if field.Known < 0 || seen[field.Known] {
			return api.ErrJSONProjection
		}
		seen[field.Known] = true
		if names[field.Known] == "charges" {
			charges, found = field.Value, true
		}
		return nil
	})
	if err != nil || !found {
		return discard()
	}
	// Bound the array before decoding into an allocated entry slice. A small
	// malformed row must not amplify three MiB into a million typed records.
	count := 0
	err = projection.Array(charges, func(api.JSONSpan) error {
		count++
		if count > api.MaxMemoryObservationChargesV1 {
			return api.ErrJSONProjection
		}
		return nil
	})
	if err != nil || !api.ExactJSONFields(raw, api.MemoryObservationV1{}) {
		return discard()
	}
	var sample api.MemoryObservationV1
	if json.Unmarshal(raw, &sample) != nil || api.ValidateMemoryObservationV1(&sample, facts) != nil {
		return discard()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &sample, nil
}
