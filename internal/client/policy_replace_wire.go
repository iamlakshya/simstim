package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type policyReplacePublicationKey struct{}

// policyReplaceBeforeRequest runs only after the real ordered writer turn.
func policyReplaceBeforeRequest(ctx context.Context, c *Conn, req proto.Request, args any) error {
	if err := policyLockdownBeforeRequestV1(ctx, c, req, args); err != nil {
		return err
	}
	o, _ := ctx.Value(policyReplacePublicationKey{}).(*policyReplaceOperation)
	if o == nil {
		if req.Op == "policy.replace" {
			return policyReplaceSourceError()
		}
		return nil
	}
	if req.Op != "policy.replace" || c != o.conn || req.ID == 0 {
		return policyReplaceSourceError()
	}
	if err := checkPolicyChange(ctx); err != nil {
		return err
	}
	held, ok := ctx.Value(heldKey{c.Host.Name}).(*heldLock)
	if !ok || held.gone.Load() {
		return policyReplaceSourceError()
	}
	if err := o.checkGrants(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.busy || o.closed || o.uncertain || o.wire == nil || o.attempts >= 3 || !reflect.DeepEqual(args, *o.wire) {
		return policyReplaceSourceError()
	}
	encoded, err := json.Marshal(*o.wire)
	if err != nil || !bytes.Equal(encoded, req.Args) {
		return policyReplaceSourceError()
	}
	o.attempts++
	return nil
}

// Apply retries only explicit hash conflicts. An uncertain send is never
// replayed, and no retry replaces the original connection, R or file source.
func (p *PolicyReplacement) Apply(ctx context.Context) (api.PolicyReplaceResultV1, error) {
	var empty api.PolicyReplaceResultV1
	if p == nil || p.operation == nil {
		return empty, policyReplaceSourceError()
	}
	if err := checkPolicyChange(ctx); err != nil {
		return empty, err
	}
	o := p.operation
	if err := o.checkLocal(ctx); err != nil {
		return empty, err
	}
	o.mu.Lock()
	if o.busy || o.closed || o.uncertain {
		o.mu.Unlock()
		return empty, policyReplaceSourceError()
	}
	if o.result != nil {
		result := *o.result
		result.NextStart = slices.Clone(result.NextStart)
		o.mu.Unlock()
		return result, nil
	}
	o.busy = true
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.busy = false; o.wire = nil; o.mu.Unlock() }()
	for attempt := 0; attempt < 3; attempt++ {
		if err := p.Check(ctx); err != nil {
			return empty, err
		}
		owned, release, err := o.conn.beginGrantSource(ctx, "replace live policy", true)
		if err != nil {
			return empty, err
		}
		if err = o.checkLocal(owned); err != nil {
			release()
			return empty, err
		}
		o.mu.Lock()
		args := api.PolicyReplaceArgsV1{Version: 1, Session: o.session, PreviousHash: o.currentHash, Layers: api.CloneLayers(o.layers)}
		o.wire = &args
		o.mu.Unlock()
		if err = o.checkGrants(owned); err != nil {
			release()
			return empty, err
		}
		owned = context.WithValue(owned, policyReplacePublicationKey{}, o)
		var raw json.RawMessage
		err = o.conn.callWithSent(owned, "policy.replace", args, &raw, release)
		if err != nil {
			o.mu.Lock()
			o.uncertain = true
			o.mu.Unlock()
			return empty, err
		}
		result, err := decodePolicyReplaceResult(raw)
		if err != nil {
			o.mu.Lock()
			o.uncertain = true
			o.mu.Unlock()
			return empty, err
		}
		if err = o.checkLocal(ctx); err != nil {
			return empty, err
		}
		if result.State == "conflict" {
			// Borrowing the same held settings rereads its original source.
			// A changed file requires a new action, never refreshed approvals.
			if _, err = o.settings.Settings(ctx); err != nil {
				return empty, err
			}
			o.mu.Lock()
			o.currentHash = result.Hash
			o.wire = nil
			o.mu.Unlock()
			continue
		}
		expected, err := api.HashPolicyLaptopLayersV1(args.Layers)
		if err != nil || result.Hash != expected {
			o.mu.Lock()
			o.uncertain = true
			o.mu.Unlock()
			return empty, policyReplaceSourceError()
		}
		o.mu.Lock()
		copy := result
		copy.NextStart = slices.Clone(result.NextStart)
		o.result = &copy
		o.currentHash = result.Hash
		o.mu.Unlock()
		return result, nil
	}
	return empty, &UserError{Msg: "the sandbox's policy changed during three apply attempts", Hint: "Read its current policy and apply again with a new request.", Code: "policy_conflict", Kind: KindRefused}
}
