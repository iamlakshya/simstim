package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// markSessionPolicyChanges is a passive comparison. It never sends a layer,
// fetches a value, changes a pin or upgrades an unsupported local record.
func (c *Conn) markSessionPolicyChanges(ctx context.Context, rows []Session) []Session {
	for at := range rows {
		rows[at].PolicyChanged = false
	}
	if c == nil || !c.HasFeature(api.FeaturePolicyLiveV1) || c.CheckSavedSource(ctx) != nil {
		return rows
	}
	owned, release, err := c.beginGrantSource(ctx, "compare selected policy files", false)
	if err != nil {
		return rows
	}
	defer release()
	operation, err := openSettingsOperation(owned)
	if err != nil {
		return rows
	}
	defer operation.Close()
	settings, err := operation.Settings(owned)
	if err != nil {
		return rows
	}
	known, err := SandboxPolicies(owned, c.Host)
	if err != nil {
		return rows
	}
	byID := make(map[string]*SandboxPolicy, len(known))
	for at := range known {
		byID[known[at].SessionID] = &known[at]
	}
	for at, row := range rows {
		if !api.ValidPolicyHashV1(row.LayersHash) || row.Wall == nil || row.SandboxOf != "" {
			continue
		}
		creation := byID[row.ID]
		if creation == nil || creation.HostID != c.HostID() || creation.FutureLineage || creation.FutureDelivery || creation.futureDeliveryReceipt || creation.futureInputOperation {
			continue
		}
		pin, err := api.DecodePolicySourcePin(creation.PolicySources)
		if err != nil || pin == nil {
			continue
		}
		hash, err := localPolicyPinHash(settings, c.Host, pin)
		if err == nil {
			rows[at].PolicyChanged = hash != row.LayersHash
		}
	}
	if operation.Revalidate(owned) != nil || c.CheckSavedSource(owned) != nil {
		for at := range rows {
			rows[at].PolicyChanged = false
		}
	}
	return rows
}

func localPolicyPinHash(settings Settings, h Host, pin *api.PolicySourcePin) (string, error) {
	if pin == nil {
		return "", policySourceStateError()
	}
	layers := make([]api.Layer, 0, len(pin.Layers))
	for _, ref := range pin.Layers {
		if ref.Source != "" || !api.ValidSelectedLayerRef(ref) {
			return "", policySourceStateError()
		}
		layer := api.Layer{Kind: ref.Kind, Name: ref.Name}
		if ref.Kind == api.LayerSession {
			layer.LayerValues = api.CloneLayerValues(pin.Session)
		}
		layers = append(layers, layer)
	}
	fresh, err := refreshPolicyLayers(settings, h, layers)
	if err != nil {
		return "", err
	}
	return api.HashPolicyLaptopLayersV1(fresh)
}

// CleanPolicySessionInfo keeps display extensions closed and owned.
func cleanPolicySessionInfo(s Session) Session {
	s.LayersHash = CleanLine(s.LayersHash)
	s.PolicyNextWall = CleanLine(s.PolicyNextWall)
	s.PolicyNextStart = cleanLines(s.PolicyNextStart, CleanLine)
	return s
}

// PolicyDisplayName adds a passive marker only to a drawn copy.
func PolicyDisplayName(s Session) string {
	if s.PolicyChanged {
		return s.Name + "~"
	}
	return s.Name
}
