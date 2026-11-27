package client

import (
	"context"
	"encoding/json"
	"reflect"
)

// General metadata readers preserve these bytes as opaque creation history.
// Only the actual original VM builder issues history; later policy verification
// interprets supported versions. Raw history never supplies a boot or grant.
func originalVMImageHistoryV1(plan *VMNeedPlanV1) (json.RawMessage, error) {
	if plan == nil || plan.imageSelection == nil || plan.conn == nil || plan.hostID != plan.conn.HostID() || !reflect.DeepEqual(plan.need.Image, plan.imageSelection.Image) {
		return nil, imageSelectionHistoryError()
	}
	return encodeSandboxImageSelection(plan.imageSelection)
}

// A direct New SDK caller may omit its materialised layers. The same original
// image policy constructor still selected local all/host/profile identities;
// pin those actual identities rather than losing them from creation history.
func originalVMImagePolicySourcesV1(ctx context.Context, a NewArgs) (json.RawMessage, error) {
	r := grantRequestOf(ctx)
	if r == nil {
		return nil, grantOperationError()
	}
	r.mu.Lock()
	p := r.imagePolicy
	r.mu.Unlock()
	if p == nil || p.family != grantOpNew {
		return nil, imageSelectionHistoryError()
	}
	if err := p.check(ctx, p.conn); err != nil {
		return nil, err
	}
	a.Layers = laptopStartLayers(p.layers)
	return encodeLocalPolicySources(a)
}
