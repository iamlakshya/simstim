package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) policyReplaceRequest(c *client, req proto.Request) bool {
	args, err := host.DecodePolicyReplace(req.Args)
	if err != nil || args.Session != s.id {
		c.reply(req.ID, nil, toProtoErr(api.ErrPolicyLiveV1))
		return true
	}
	result, perr := s.replaceLaptopPolicy(args)
	c.reply(req.ID, result, perr)
	return true
}

// replaceLaptopPolicy holds the same policy turn as session changes. Pending
// values are data only; no native Need, secret fetch or boot path runs here.
func (s *Supervisor) replaceLaptopPolicy(args api.PolicyReplaceArgsV1) (api.PolicyReplaceResultV1, *proto.Error) {
	var empty api.PolicyReplaceResultV1
	if args.Validate() != nil || args.Session != s.id {
		return empty, toProtoErr(api.ErrPolicyLiveV1)
	}
	s.polMu.Lock()
	defer s.polMu.Unlock()
	s.mu.Lock()
	before := api.CloneLayers(s.m.Layers)
	previousNext := slices.Clone(s.m.PolicyNext)
	var applied api.Policy
	if s.m.Policy != nil {
		applied = api.ClonePolicy(*s.m.Policy)
	}
	ep, ref := s.eg, sessionRefOf(s.m)
	s.mu.Unlock()
	current, err := api.DesiredPolicyLayersV1(before, previousNext)
	if err != nil {
		return empty, toProtoErr(err)
	}
	hash, err := api.HashPolicyLaptopLayersV1(current)
	if err != nil {
		return empty, toProtoErr(err)
	}
	result := api.PolicyReplaceResultV1{Version: api.PolicyReplaceVersionV1, Hash: hash, State: "conflict"}
	if hash != args.PreviousHash {
		return result, nil
	}
	desired, err := api.ReplacePolicyLaptopLayersV1(current, args.Layers)
	if err != nil {
		return empty, toProtoErr(err)
	}
	live, nextHash, _, err := egress.PrepareLivePolicyV1(applied, desired)
	if err != nil {
		return empty, toProtoErr(err)
	}
	layers, nextStart, err := egress.ProjectLivePolicyLayersV1(before, desired)
	if err != nil {
		return empty, toProtoErr(err)
	}
	result = api.PolicyReplaceResultV1{Version: api.PolicyReplaceVersionV1, Hash: nextHash, State: "unchanged", NextStart: nextStart}
	if nextHash == hash {
		return result, nil
	}
	if ep == nil || ep.dead.Load() || ep.ctl == nil || channelClosed(ep.ctl.done) {
		return empty, toProtoErr(policyReplaceUnavailable())
	}
	capsule, err := api.EncodePolicyNextLayersV1(args.Layers)
	if err != nil {
		return empty, toProtoErr(err)
	}
	if api.PolicyNextMatchesAppliedV1(layers, capsule) {
		capsule = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pushReplacementToEgress(ctx, ep, ref, live, desired, nextHash); err != nil {
		return empty, toProtoErr(err)
	}
	s.saveMu.Lock()
	var snapshot host.Manifest
	change := func(target *host.Manifest) error {
		s.mu.Lock()
		owned := s.eg == ep && !ep.dead.Load() && !channelClosed(ep.ctl.done)
		s.mu.Unlock()
		if !owned || !bytes.Equal(target.PolicyNext, previousNext) {
			return policyReplaceUnavailable()
		}
		now, err := api.DesiredPolicyLayersV1(target.Layers, target.PolicyNext)
		if err != nil {
			return err
		}
		actual, err := api.HashPolicyLaptopLayersV1(now)
		if err != nil || actual != hash {
			return policyReplaceUnavailable()
		}
		target.Policy, target.Layers, target.PolicyNext = &live, api.CloneLayers(layers), slices.Clone(capsule)
		return nil
	}
	publish := func(target *host.Manifest) {
		policy := api.ClonePolicy(*target.Policy)
		s.m.Policy, s.m.Layers, s.m.PolicyNext = &policy, api.CloneLayers(target.Layers), slices.Clone(target.PolicyNext)
	}
	err = s.writeManifestChange(&snapshot, false, nil, nil, change, publish)
	s.saveMu.Unlock()
	if err != nil {
		s.failPolicySave(ep)
		return empty, toProtoErr(err)
	}
	s.audit.write("policy_replace", map[string]any{"hash": nextHash, "next_start": nextStart})
	result.State = "applied"
	return result, nil
}

func policyReplaceUnavailable() error {
	return &proto.Error{Code: "policy_not_running", Message: "the original live policy service is no longer available", Hint: "Read the sandbox's current policy after resuming it, then apply again."}
}

func pushReplacementToEgress(ctx context.Context, ep *egressProc, ref string, policy api.Policy, sources []api.Layer, expected string) error {
	var raw json.RawMessage
	args := egress.PolicyUpdateArgs{SessionRef: ref, Policy: api.ClonePolicy(policy), SourceLayers: api.CloneLayers(sources)}
	if err := ep.ctl.request(ctx, egress.CtlPolicyUpdate, args, &raw); err != nil {
		ep.ctl.closeOwnedControl()
		return err
	}
	var result struct {
		Hash string `json:"hash"`
	}
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Hash != expected {
		ep.ctl.closeOwnedControl()
		return policyReplaceUnavailable()
	}
	return nil
}
