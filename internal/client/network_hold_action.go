package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// AnswerNetworkRequest asks the original live actor to allow once or for
// this session. Its private operation derives the actual restriction/frame;
// callers supply only V1, session, opaque request ID and requested scope.
func (c *Conn) AnswerNetworkRequest(ctx context.Context, args api.NetworkHoldActionArgs) (_ api.NetworkHoldAnswerResult, returned error) {
	defer func() { returned = networkHoldClientError(returned) }()
	operation, err := newNetworkHoldOperation(ctx, c, args)
	if err != nil {
		return api.NetworkHoldAnswerResult{}, err
	}
	return operation.answer(ctx)
}

// ResolveNetworkRequest returns a transient non-sensitive wider-scope
// description. Its Target is never local publication permission. The same
// original operation must perform PublishNetworkRequest after any question.
func (c *Conn) ResolveNetworkRequest(ctx context.Context, args api.NetworkHoldActionArgs) (_ api.NetworkHoldResolution, returned error) {
	defer func() { returned = networkHoldClientError(returned) }()
	operation, err := newNetworkHoldOperation(ctx, c, args)
	if err != nil {
		return api.NetworkHoldResolution{}, err
	}
	return operation.resolve(ctx)
}

// PublishNetworkRequest uses the original opaque-ID operation's guarded file
// writer. It never passes a returned display/resolution Target to a new write.
func (c *Conn) PublishNetworkRequest(ctx context.Context, args api.NetworkHoldActionArgs, layer FileLayer) (_ FilePolicyResult, returned error) {
	defer func() { returned = networkHoldClientError(returned) }()
	operation, err := newNetworkHoldOperation(ctx, c, args)
	if err != nil {
		return FilePolicyResult{}, err
	}
	return operation.publish(ctx, layer)
}
