package client

import (
	"context"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// NetworkHoldFileLayerV1 reads the original laptop's selected repository or
// profile name for a policy-sheet action. The returned name is local selection
// metadata. It supplies no resolved network target or publication permission.
func (c *Conn) NetworkHoldFileLayerV1(ctx context.Context, session string, scope api.NetworkHoldScope) (FileLayer, error) {
	if scope != api.NetworkHoldRepo && scope != api.NetworkHoldProfile {
		return FileLayer{}, networkHoldArgumentError()
	}
	if err := c.networkHoldNonpositiveEntry(ctx, session, ""); err != nil {
		return FileLayer{}, err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return FileLayer{}, err
	}
	_, creation, err := EffectiveSessionTier(ctx, c.Host, session, c.HostID())
	if err != nil {
		return FileLayer{}, err
	}
	if creation == nil {
		return FileLayer{}, networkHoldMissingLayerV1()
	}
	pin, err := api.DecodePolicySourcePin(creation.PolicySources)
	if err != nil || pin == nil {
		return FileLayer{}, policySourceStateError()
	}
	kind := api.LayerRepo
	if scope == api.NetworkHoldProfile {
		kind = api.LayerProfile
	}
	var selected FileLayer
	for _, layer := range pin.Layers {
		if layer.Kind == kind && layer.Source != api.SourceRepositoryFile {
			// The sheet's repository key names this repository. A selected
			// organisation pattern cannot silently broaden that answer.
			if kind == api.LayerRepo && strings.ContainsAny(layer.Name, "*?[") {
				continue
			}
			selected = FileLayer{Kind: kind, Name: layer.Name}
		}
	}
	if selected.Name == "" {
		return FileLayer{}, networkHoldMissingLayerV1()
	}
	return selected, c.CheckSavedSource(ctx)
}

func networkHoldMissingLayerV1() error {
	return &UserError{Msg: "this laptop has no original exact policy layer for that scope",
		Hint: "Choose once or session, or name an explicit repository or profile in the policy command.", Code: "network_hold_layer", Kind: KindRefused}
}
