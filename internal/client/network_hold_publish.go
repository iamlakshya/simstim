package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// A positive file action accepts no public resolution back from its caller.
// Only the complete response retained by resolve can reach the native writer.
func (o *networkHoldOperation) publish(ctx context.Context, layer FileLayer) (FilePolicyResult, error) {
	if layer != o.selected || o.args.Scope != api.NetworkHoldRepo && o.args.Scope != api.NetworkHoldProfile {
		return FilePolicyResult{}, networkHoldSourceError()
	}
	if _, err := o.resolve(ctx); err != nil {
		return FilePolicyResult{}, err
	}
	if err := o.start(ctx); err != nil {
		return FilePolicyResult{}, err
	}
	defer o.finish()
	o.mu.Lock()
	ended := o.attempted || o.published
	o.mu.Unlock()
	if ended {
		return FilePolicyResult{}, networkHoldSourceError()
	}
	owned := context.WithValue(ctx, networkHoldFilePublicationKey{}, o)
	result, err := persistResolvedNetworkHoldV1(owned, o)
	if err != nil {
		return FilePolicyResult{}, err
	}
	o.mu.Lock()
	o.published = true
	o.mu.Unlock()
	return result, nil
}

// The native config transaction calls this after questions, immediately before
// its actual replace. An uncertain publication cannot be replayed as a fresh one.
func (o *networkHoldOperation) beforeFilePublicationV1(ctx context.Context) error {
	if current, _ := ctx.Value(networkHoldFilePublicationKey{}).(*networkHoldOperation); current != o {
		return networkHoldSourceError()
	}
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !o.busy || o.attempted || o.published || o.descriptor == nil || o.resolution == nil ||
		o.descriptor.Reason == api.NetworkHoldUploadBudget || o.resolution.Class != api.NetworkHoldPublicTarget ||
		o.resolution.Request != o.args.Request || o.resolution.Scope != o.args.Scope ||
		o.resolution.Revision != o.descriptor.Revision || !o.resolution.Expires.Equal(o.descriptor.Expires) {
		return networkHoldSourceError()
	}
	o.attempted = true
	return nil
}

func requireNetworkHoldOutboundV1(ctx context.Context, request *GrantRequest, outbound Outbound) error {
	o, _ := ctx.Value(networkHoldFilePublicationKey{}).(*networkHoldOperation)
	if o == nil || request == nil || request != grantRequestOf(ctx) || outbound.Kind != OutboundNet {
		return grantPurposeError()
	}
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !o.busy || o.request != request || o.published || o.resolution == nil || o.descriptor == nil ||
		o.descriptor.Reason == api.NetworkHoldUploadBudget || o.resolution.Target != outbound.Name || o.resolution.Class != api.NetworkHoldPublicTarget ||
		o.args.Scope != api.NetworkHoldRepo && o.args.Scope != api.NetworkHoldProfile {
		return grantPurposeError()
	}
	return nil
}
