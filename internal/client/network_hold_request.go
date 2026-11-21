package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// NewNetworkHoldActionRequestV1 captures one positive action before Dial or
// questions. The selected file layer is original local intent. A descriptor
// or a returned target cannot create or change this private operation.
func NewNetworkHoldActionRequestV1(args api.NetworkHoldActionArgs, selected FileLayer) (*GrantRequest, error) {
	if args.Version != api.NetworkHoldV1 || !api.ValidSessionID(args.Session) || !api.ValidNetworkHoldID(args.Request) ||
		args.Revision != 0 || args.Maximum != "" || !validHoldActionScope(args.Scope) {
		return nil, networkHoldArgumentError()
	}
	if args.Scope == api.NetworkHoldRepo || args.Scope == api.NetworkHoldProfile {
		kind := api.LayerRepo
		if args.Scope == api.NetworkHoldProfile {
			kind = api.LayerProfile
		}
		if selected.Kind != kind || selected.Name == "" || len(selected.Name) > 4096 || CleanLine(selected.Name) != selected.Name {
			return nil, networkHoldArgumentError()
		}
		if kind == api.LayerRepo {
			origin, err := NormaliseOrigin(selected.Name)
			if err != nil || origin != selected.Name {
				return nil, networkHoldArgumentError()
			}
		}
	} else if selected != (FileLayer{}) {
		return nil, networkHoldArgumentError()
	}
	r := NewNetworkHoldGrantRequest()
	r.networkHold = &networkHoldOperation{request: r, args: args, selected: selected}
	return r, nil
}

// NewNetworkHoldGrantRequest retains one held-request action's original
// intent before Dial or questions. It grants no target, value, namespace or
// private-address exception, and cannot borrow another operation's answers.
func NewNetworkHoldGrantRequest() *GrantRequest {
	return &GrantRequest{purpose: grantNetworkHold, operation: grantOpNetworkHold}
}

// Read/refuse may have no request. An existing request must belong to this
// closed action purpose; absence never grants a positive answer or resolve.
func requireNetworkHoldRequestContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return err
	}
	if r.purpose != grantNetworkHold || r.operation != grantOpNetworkHold || r.SSHAgent || r.Untrusted {
		return grantPurposeError()
	}
	return nil
}
