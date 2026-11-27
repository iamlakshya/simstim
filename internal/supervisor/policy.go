package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) policyRequest(c *client, req proto.Request) bool {
	if req.Op == "policy.lockdown" {
		return s.policyLockdownRequestV1(c, req)
	}
	if req.Op == "policy.replace" {
		return s.policyReplaceRequest(c, req)
	}
	switch req.Op {
	case "policy.ls":
		s.mu.Lock()
		layers := api.CloneLayers(s.m.Layers)
		next := slices.Clone(s.m.PolicyNext)
		s.mu.Unlock()
		policy, err := host.PolicySnapshotWithNext(layers, next)
		if err != nil {
			c.reply(req.ID, nil, toProtoErr(err))
			return true
		}
		c.reply(req.ID, policy, nil)
		return true
	case "policy.log":
		var a api.PolicyLogArgs
		if len(req.Args) > 0 && json.Unmarshal(req.Args, &a) != nil {
			c.reply(req.ID, nil, &proto.Error{Code: "bad_request", Message: "invalid policy.log arguments", Hint: "Update the client."})
			return true
		}
		a.ID = s.id
		entries, err := host.ReadPolicyLog(paths.AuditLog(s.id), a)
		if err != nil {
			c.reply(req.ID, nil, toProtoErr(err))
			return true
		}
		c.reply(req.ID, entries, nil)
	case "policy.add":
		var a api.PolicyAddArgs
		if json.Unmarshal(req.Args, &a) != nil {
			c.reply(req.ID, nil, &proto.Error{Code: "bad_request", Message: "invalid policy.add arguments", Hint: "Update the client."})
			return true
		}
		rule, perr := s.applyLivePolicyAdd(a)
		c.reply(req.ID, rule, perr)
	case "policy.rm":
		var a api.PolicyRmArgs
		if json.Unmarshal(req.Args, &a) != nil {
			c.reply(req.ID, nil, &proto.Error{Code: "bad_request", Message: "invalid policy.rm arguments", Hint: "Update the client."})
			return true
		}
		removed, perr := s.applyLivePolicyRm(a)
		if perr != nil {
			c.reply(req.ID, nil, perr)
			return true
		}
		c.reply(req.ID, map[string]int{"removed": removed}, nil)
	}
	return true
}

func (s *Supervisor) applyLivePolicyAdd(a api.PolicyAddArgs) (*api.Rule, *proto.Error) {
	s.polMu.Lock()
	defer s.polMu.Unlock()

	s.mu.Lock()
	curLayers := slices.Clone(s.m.Layers)
	ref := sessionRefOf(s.m)
	ep := s.eg
	s.mu.Unlock()

	pol, layers, rule, err := host.ApplyPolicyAdd(curLayers, a)
	if err != nil {
		return nil, toProtoErr(err)
	}
	if perr := pushPolicyToEgress(ep, ref, pol); perr != nil {
		return nil, perr
	}
	s.mu.Lock()
	s.m.Policy, s.m.Layers = &pol, layers
	snap := s.snapshot()
	s.mu.Unlock()
	if err := s.save(&snap, false); err != nil {
		s.failPolicySave(ep)
		return nil, toProtoErr(err)
	}
	if err := s.reconcilePorts(); err != nil {
		return nil, toProtoErr(err)
	}
	s.audit.write("rule", map[string]any{"op": "add", "id": rule.ID, "allow": rule.Allow, "target": rule.Target, "once": rule.Once, "door": rule.Door, "mode": rule.Mode, "tools": rule.Tools, "methods": rule.Methods, "public_only": rule.PublicOnly, "grant_id": rule.GrantID, "until": rule.Until})
	return &rule, nil
}

func (s *Supervisor) applyLivePolicyRm(a api.PolicyRmArgs) (int, *proto.Error) {
	s.polMu.Lock()
	defer s.polMu.Unlock()

	s.mu.Lock()
	curLayers := slices.Clone(s.m.Layers)
	ref := sessionRefOf(s.m)
	ep := s.eg
	s.mu.Unlock()

	pol, layers, removed, err := host.ApplyPolicyRm(curLayers, a)
	if err != nil {
		return 0, toProtoErr(err)
	}
	if perr := pushPolicyToEgress(ep, ref, pol); perr != nil {
		return 0, perr
	}
	s.mu.Lock()
	s.m.Policy, s.m.Layers = &pol, layers
	snap := s.snapshot()
	s.mu.Unlock()
	if err := s.save(&snap, false); err != nil {
		s.failPolicySave(ep)
		return 0, toProtoErr(err)
	}
	if err := s.reconcilePorts(); err != nil {
		return 0, toProtoErr(err)
	}
	s.audit.write("rule", map[string]any{"op": "rm", "id": a.ID, "target": a.Target, "removed": removed})
	return removed, nil
}

func pushPolicyToEgress(ep *egressProc, ref string, pol api.Policy) *proto.Error {
	if ep == nil || ep.dead.Load() || ep.ctl == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := requirePublicOnlyEgress(ctx, ep, pol); err != nil {
		return toProtoErr(err)
	}
	if err := ep.ctl.request(ctx, egress.CtlPolicyUpdate, egress.PolicyUpdateArgs{SessionRef: ref, Policy: pol, ResetUnknownRefusals: true}, nil); err != nil {
		return toProtoErr(err)
	}
	return nil
}

func toProtoErr(err error) *proto.Error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe
	}
	return &proto.Error{Code: "policy_error", Message: err.Error(), Hint: "Check the policy rule and try again."}
}

func (s *Supervisor) sharedPolicyRequest(c *client, req proto.Request) bool {
	if s.networkHoldRequest(c, req) {
		return true
	}
	switch req.Op {
	case "policy.ls", "policy.log", "policy.add", "policy.rm", "policy.replace", "policy.lockdown":
		s.owner().policyRequest(c, req)
		return true
	}
	return false
}
