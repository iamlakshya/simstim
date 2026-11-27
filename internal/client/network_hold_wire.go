package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nettarget"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (o *networkHoldOperation) call(ctx context.Context, operation string, args any, out any) error {
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	o.phase = operation
	o.mu.Unlock()
	owned, release, err := o.conn.beginGrantSource(ctx, "held network request", true)
	if err != nil {
		return err
	}
	defer release()
	if err := o.checkLocal(owned); err != nil {
		return err
	}
	owned = context.WithValue(owned, networkHoldPublicationKey{}, o)
	if err := o.conn.callWithSent(owned, operation, args, out, release); err != nil {
		return err
	}
	return o.checkLocal(ctx)
}

func (o *networkHoldOperation) beforeRequest(ctx context.Context, c *Conn, request proto.Request, args any) error {
	if o == nil || c != o.conn {
		return networkHoldSourceError()
	}
	held, ok := ctx.Value(heldKey{c.Host.Name}).(*heldLock)
	if !ok || held.gone.Load() {
		return networkHoldSourceError()
	}
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	phase, busy := o.phase, o.busy
	o.mu.Unlock()
	if !busy || phase != request.Op {
		return networkHoldSourceError()
	}
	switch request.Op {
	case api.OpPolicyDescribe:
		expected := api.NetworkHoldDescribeArgs{Version: api.NetworkHoldV1, Session: o.args.Session, Request: o.args.Request}
		if !reflect.DeepEqual(args, expected) {
			return networkHoldSourceError()
		}
	case api.OpPolicyAnswer, api.OpPolicyResolve:
		expected := o.positiveArgs()
		if !api.ValidNetworkHoldPositiveAction(expected) || !reflect.DeepEqual(args, expected) {
			return networkHoldSourceError()
		}
		o.mu.Lock()
		descriptor := o.descriptor
		o.mu.Unlock()
		if descriptor == nil || descriptor.Class != api.NetworkHoldPublicTarget {
			return networkHoldSourceError()
		}
		if descriptor.Reason == api.NetworkHoldUploadBudget && o.args.Scope != api.NetworkHoldOnce {
			return networkHoldUploadScopeErrorV1()
		}
		if request.Op == api.OpPolicyAnswer {
			if o.args.Scope != api.NetworkHoldOnce && o.args.Scope != api.NetworkHoldSession {
				return networkHoldSourceError()
			}
			o.mu.Lock()
			if o.attempted {
				o.mu.Unlock()
				return networkHoldSourceError()
			}
			o.attempted = true
			o.mu.Unlock()
		} else if o.args.Scope != api.NetworkHoldRepo && o.args.Scope != api.NetworkHoldProfile {
			return networkHoldSourceError()
		}
	default:
		return networkHoldSourceError()
	}
	encoded, err := json.Marshal(args)
	if err != nil || !bytes.Equal(encoded, request.Args) {
		return networkHoldSourceError()
	}
	return nil
}

func holdResponseFields(raw json.RawMessage, shape any) (map[string]json.RawMessage, error) {
	fields, err := api.JSONObjectFields(raw, api.ExactJSONFieldNames(shape)...)
	if err != nil {
		return nil, networkHoldPositiveProtocolErrorV1()
	}
	var version int
	if json.Unmarshal(fields["v"], &version) != nil {
		return nil, networkHoldPositiveProtocolErrorV1()
	}
	if version != api.NetworkHoldV1 {
		return nil, &UserError{Msg: "this held-request response uses a newer format", Hint: "Upgrade the client before answering this request.", Code: "network_holds_future", Kind: KindRefused}
	}
	return fields, nil
}
func closedHoldResponse(raw json.RawMessage, shape any) error {
	if !api.ExactJSONFields(raw, shape) {
		return &UserError{Msg: "this held-request response has unsupported extensions", Hint: "Upgrade the client before answering this request.", Code: "network_holds_future", Kind: KindRefused}
	}
	return nil
}
func decodeHoldDescriptor(raw json.RawMessage, id string) (api.NetworkHoldDescriptor, error) {
	var result api.NetworkHoldDescriptor
	if _, err := holdResponseFields(raw, result); err != nil {
		return result, err
	}
	if json.Unmarshal(raw, &result) != nil || !api.ValidNetworkHoldDescriptor(result, id) || !time.Now().Before(result.Expires) {
		return result, networkHoldPositiveProtocolErrorV1()
	}
	return result, closedHoldResponse(raw, result)
}

func (o *networkHoldOperation) answer(ctx context.Context) (api.NetworkHoldAnswerResult, error) {
	var empty api.NetworkHoldAnswerResult
	if o.args.Scope != api.NetworkHoldOnce && o.args.Scope != api.NetworkHoldSession {
		return empty, networkHoldSourceError()
	}
	if err := o.start(ctx); err != nil {
		return empty, err
	}
	defer o.finish()
	o.mu.Lock()
	cached, attempted := o.answerResult, o.attempted
	o.mu.Unlock()
	if cached != nil {
		out := *cached
		out.RuleIDs = slices.Clone(out.RuleIDs)
		return out, o.checkLocal(ctx)
	}
	if attempted {
		return empty, networkHoldSourceError()
	}
	if err := o.prepare(ctx); err != nil {
		return empty, err
	}
	var raw json.RawMessage
	if err := o.call(ctx, api.OpPolicyAnswer, o.positiveArgs(), &raw); err != nil {
		return empty, err
	}
	var result api.NetworkHoldAnswerResult
	if _, err := holdResponseFields(raw, result); err != nil {
		return empty, err
	}
	if json.Unmarshal(raw, &result) != nil || result.Scope != o.args.Scope {
		return empty, networkHoldPositiveProtocolErrorV1()
	}
	if result.MemoryOnly {
		// The real actor returns one opaque group, however many flows wait.
		if len(result.RuleIDs) != 1 || !api.ValidNetworkHoldID(result.RuleIDs[0]) {
			return empty, networkHoldPositiveProtocolErrorV1()
		}
	} else if result.Scope != api.NetworkHoldSession || len(result.RuleIDs) != 0 {
		// The actual durable SESSION writer acknowledges without publishing a
		// target or rule identifier on this answer channel.
		return empty, networkHoldPositiveProtocolErrorV1()
	}
	if err := closedHoldResponse(raw, result); err != nil {
		return empty, err
	}
	result.RuleIDs = slices.Clone(result.RuleIDs)
	o.mu.Lock()
	copy := result
	copy.RuleIDs = slices.Clone(result.RuleIDs)
	o.answerResult = &copy
	o.mu.Unlock()
	return result, o.checkLocal(ctx)
}

func (o *networkHoldOperation) resolve(ctx context.Context) (api.NetworkHoldResolution, error) {
	var empty api.NetworkHoldResolution
	if o.args.Scope != api.NetworkHoldRepo && o.args.Scope != api.NetworkHoldProfile {
		return empty, networkHoldSourceError()
	}
	if err := o.start(ctx); err != nil {
		return empty, err
	}
	defer o.finish()
	o.mu.Lock()
	cached := o.resolution
	o.mu.Unlock()
	if cached != nil {
		return *cached, o.checkLocal(ctx)
	}
	if err := o.prepare(ctx); err != nil {
		return empty, err
	}
	var raw json.RawMessage
	if err := o.call(ctx, api.OpPolicyResolve, o.positiveArgs(), &raw); err != nil {
		return empty, err
	}
	var result api.NetworkHoldResolution
	if _, err := holdResponseFields(raw, result); err != nil {
		return empty, err
	}
	o.mu.Lock()
	descriptor := *o.descriptor
	o.mu.Unlock()
	if json.Unmarshal(raw, &result) != nil {
		return empty, networkHoldPositiveProtocolErrorV1()
	}
	target, err := nettarget.Parse(result.Target)
	if err != nil || target.String() != result.Target || result.Request != o.args.Request || result.Scope != o.args.Scope || result.Revision != descriptor.Revision || result.Class != descriptor.Class || !result.Expires.Equal(descriptor.Expires) || !time.Now().Before(result.Expires) {
		return empty, networkHoldPositiveProtocolErrorV1()
	}
	if target.Prefix.IsValid() && !heldPublicLiteralV1(target) {
		return empty, networkHoldSourceError()
	}
	if err := closedHoldResponse(raw, result); err != nil {
		return empty, err
	}
	// Only a COMPLETE actual response from the original pending call enters
	// this private capsule. A public resolution value cannot install it.
	o.mu.Lock()
	copy := result
	o.resolution = &copy
	o.mu.Unlock()
	return result, o.checkLocal(ctx)
}
