package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Positive wire actions require the original private operation even when a
// generic caller supplies otherwise well-formed public arguments.
func networkHoldBeforeRequestV1(ctx context.Context, conn *Conn, request proto.Request, args any) error {
	if request.Op != api.OpPolicyDescribe && request.Op != api.OpPolicyAnswer && request.Op != api.OpPolicyResolve {
		return nil
	}
	operation, _ := ctx.Value(networkHoldPublicationKey{}).(*networkHoldOperation)
	grant := grantRequestOf(ctx)
	if operation == nil || grant == nil || operation.request != grant || operation.conn != conn {
		return networkHoldSourceError()
	}
	grant.mu.Lock()
	matches := grant.networkHold == operation && grant.purpose == grantNetworkHold && grant.operation == grantOpNetworkHold && !grant.ending && !grant.preparationClosed
	grant.mu.Unlock()
	if !matches {
		return networkHoldSourceError()
	}
	return operation.beforeRequest(ctx, conn, request, args)
}
