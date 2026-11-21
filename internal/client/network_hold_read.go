package client

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// PendingNetworkRequests reads a complete display snapshot on the original
// accepted session connection. Its public Host fields supply no target.
func (c *Conn) PendingNetworkRequests(ctx context.Context, session string) (_ api.NetworkHoldSnapshot, returned error) {
	defer func() { returned = networkHoldClientError(returned) }()
	if err := c.networkHoldNonpositiveEntry(ctx, session, ""); err != nil {
		return api.NetworkHoldSnapshot{}, err
	}
	owned, release, err := c.beginGrantSource(ctx, "read pending network requests", true)
	if err != nil {
		return api.NetworkHoldSnapshot{}, err
	}
	var raw json.RawMessage
	args := api.NetworkHoldQueryArgs{Version: api.NetworkHoldV1, Session: session}
	if err := c.callWithSent(owned, api.OpPolicyPending, args, &raw, release); err != nil {
		return api.NetworkHoldSnapshot{}, err
	}
	if err := requireNetworkHoldRequestContext(ctx); err != nil {
		return api.NetworkHoldSnapshot{}, err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return api.NetworkHoldSnapshot{}, err
	}
	return decodeNetworkHoldSnapshot(raw)
}

// RefuseNetworkRequest closes only an original live opaque ID. It grants no
// allowance, records no names or values, and cannot renew an ended Source.
func (c *Conn) RefuseNetworkRequest(ctx context.Context, session, request string) (returned error) {
	defer func() { returned = networkHoldClientError(returned) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !api.ValidNetworkHoldID(request) {
		return networkHoldArgumentError()
	}
	if err := c.networkHoldNonpositiveEntry(ctx, session, request); err != nil {
		return err
	}
	owned, release, err := c.beginGrantSource(ctx, "refuse pending network request", true)
	if err != nil {
		return err
	}
	args := api.NetworkHoldActionArgs{Version: api.NetworkHoldV1, Session: session, Request: request}
	return c.callWithSent(owned, api.OpPolicyRefuse, args, nil, release)
}

func (c *Conn) networkHoldNonpositiveEntry(ctx context.Context, session, request string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !api.ValidSessionID(session) || request != "" && !api.ValidNetworkHoldID(request) {
		return networkHoldArgumentError()
	}
	if c == nil || !c.HasFeature(api.FeatureNetworkHolds) {
		return &UserError{Code: "feature_missing", Kind: KindFailed,
			Msg: "the host helper cannot read live held network requests", Hint: "Upgrade the helper before reading or answering held requests."}
	}
	if err := requireNetworkHoldRequestContext(ctx); err != nil {
		return err
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return err
	}
	if c.agentOnly || c.hostTransport.kind != hostTransportSaved || !ValidHostID(c.HostID()) || c.HostIDChanged() {
		return grantOperationError()
	}
	return nil
}

func decodeNetworkHoldSnapshot(raw json.RawMessage) (api.NetworkHoldSnapshot, error) {
	if !api.ExactJSONFields(raw, api.NetworkHoldSnapshot{}) {
		return api.NetworkHoldSnapshot{}, networkHoldProtocolError()
	}
	var envelope struct {
		Requests []json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Requests) > api.NetworkHoldCapacity {
		return api.NetworkHoldSnapshot{}, networkHoldProtocolError()
	}
	for _, request := range envelope.Requests {
		if !api.ExactJSONFields(request, api.PendingNetworkRequest{}) {
			return api.NetworkHoldSnapshot{}, networkHoldProtocolError()
		}
	}
	var snapshot api.NetworkHoldSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || !api.ValidNetworkHoldSnapshot(snapshot) {
		return api.NetworkHoldSnapshot{}, networkHoldProtocolError()
	}
	return snapshot, nil
}

func networkHoldArgumentError() error {
	return &UserError{Code: "policy_pending_target", Kind: KindUsage,
		Msg: "a held request needs its exact session and opaque request ID", Hint: "Read the current pending requests and use their unchanged IDs."}
}

func networkHoldProtocolError() error {
	return &UserError{Code: "policy_pending_invalid", Kind: KindFailed,
		Msg: "the host returned an invalid complete held-request snapshot", Hint: "Upgrade the helper and read the pending requests again."}
}

func networkHoldClientError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	var remote *proto.Error
	if errors.As(err, &remote) {
		return &UserError{Code: remote.Code, Msg: CleanLine(remote.Message), Hint: CleanLine(remote.Hint), Kind: HostKind(remote.Code)}
	}
	return &UserError{Code: "policy_pending", Kind: KindFailed,
		Msg: "could not read or refuse the held request: " + CleanLine(err.Error()), Hint: "Check the host connection, then read the current pending requests again."}
}
