package supervisor

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// requireBoundGuest queries the actual owned guest before any new-form
// start/spawn frame. A bridge/helper claim cannot stand in for this receipt.
func (s *Supervisor) requireBoundGuest(ctx context.Context) error {
	root := s.owner()
	if root.ctl == nil {
		return execRefusal("the actual guest has no bound-secret capability receipt")
	}
	var raw json.RawMessage
	if err := root.ctl.request(ctx, proto.CtlGuestCapabilities, struct{}{}, &raw); err != nil {
		return err
	}
	var features proto.CtlGuestFeatures
	if !api.ExactJSONFields(raw, features) || json.Unmarshal(raw, &features) != nil || features.Version != 1 || !slices.Contains(features.Features, api.FeatureBoundSecrets) {
		return execRefusal("the actual guest cannot accept bound-secret environment modes")
	}
	return nil
}
