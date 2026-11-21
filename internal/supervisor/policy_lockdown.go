package supervisor

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) policyLockdownRequestV1(c *client, req proto.Request) bool {
	args, err := host.DecodePolicyLockdownV1(req.Args)
	if err != nil || args.Session != s.id {
		c.reply(req.ID, nil, toProtoErr(api.ErrPolicyLockdownV1))
		return true
	}
	result, err := s.applyPolicyLockdownV1(args)
	c.reply(req.ID, result, toProtoErrorOrNilV1(err))
	return true
}

func toProtoErrorOrNilV1(err error) *proto.Error {
	if err == nil {
		return nil
	}
	return toProtoErr(err)
}

func (s *Supervisor) applyPolicyLockdownV1(args api.PolicyLockdownArgsV1) (api.PolicyLockdownResultV1, error) {
	var empty api.PolicyLockdownResultV1
	if args.Validate() != nil || args.Session != s.id {
		return empty, api.ErrPolicyLockdownV1
	}
	s.polMu.Lock()
	defer s.polMu.Unlock()
	s.mu.Lock()
	before := api.CloneLayers(s.m.Layers)
	ep, ref, generation := s.eg, sessionRefOf(s.m), s.m.Run.Generation
	s.mu.Unlock()
	hash, err := api.HashPolicyLayerVectorV1(before)
	if err != nil {
		return empty, err
	}
	result := api.PolicyLockdownResultV1{Version: 1, On: args.On, Session: s.id, State: "conflict", Hash: hash}
	if hash != args.PreviousHash {
		return result, nil
	}
	policy, layers, err := host.ApplyPolicyLockdownLayersV1(before, args.On)
	if err != nil {
		return empty, err
	}
	nextHash, err := api.HashPolicyLayerVectorV1(layers)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := requireEgressLockdownV1(ctx, ep); err != nil {
		return empty, err
	}
	laptopHash, err := api.HashPolicyLaptopLayersV1(layers)
	if err != nil {
		return empty, err
	}
	if err := pushReplacementToEgress(ctx, ep, ref, policy, layers, laptopHash); err != nil {
		s.failPolicySave(ep)
		return empty, err
	}
	s.saveMu.Lock()
	var snapshot host.Manifest
	change := func(target *host.Manifest) error {
		s.mu.Lock()
		owned := s.eg == ep && !ep.dead.Load() && !channelClosed(ep.ctl.done) && s.m.Run.Generation == generation
		s.mu.Unlock()
		if !owned || target.Run.Generation != generation || !reflect.DeepEqual(target.Layers, before) {
			return policyReplaceUnavailable()
		}
		target.Policy, target.Layers = &policy, api.CloneLayers(layers)
		return nil
	}
	publish := func(target *host.Manifest) {
		copy := api.ClonePolicy(*target.Policy)
		s.m.Policy, s.m.Layers = &copy, api.CloneLayers(target.Layers)
	}
	err = s.writeManifestChange(&snapshot, false, nil, nil, change, publish)
	s.saveMu.Unlock()
	if err != nil {
		s.failPolicySave(ep)
		return empty, err
	}
	s.audit.write("lockdown", map[string]any{"on": args.On, "hash": nextHash})
	result.State, result.Hash = "applied", nextHash
	if hash == nextHash {
		result.State = "unchanged"
	}
	return result, nil
}

func requireEgressLockdownV1(ctx context.Context, ep *egressProc) error {
	if ep == nil || ep.dead.Load() || ep.ctl == nil || channelClosed(ep.ctl.done) {
		return policyReplaceUnavailable()
	}
	var raw json.RawMessage
	if err := ep.ctl.request(ctx, egress.CtlCapabilities, nil, &raw); err != nil {
		return err
	}
	var result egress.CapabilitiesResult
	fields, err := api.JSONObjectFields(raw, "features")
	if err != nil || len(fields) != 1 || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || len(result.Features) > 64 || !slices.Contains(result.Features, api.FeaturePolicyLockdownV1) {
		return host.UserError("upgrade_required", "the actual egress actor cannot apply lockdown", "Upgrade the helper, then stop and resume the sandbox before retrying lockdown.")
	}
	return nil
}
