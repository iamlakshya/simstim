package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/nettarget"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The private receiver obtains the target only from the SAME actual actor
// control response to the original full action. No public target argument,
// display row or reusable constructor can enter this policy writer.
func (s *Supervisor) resolveNetworkHoldV1(ctx context.Context, args api.NetworkHoldActionArgs) (api.NetworkHoldResolution, *proto.Error) {
	return s.resolveNetworkHoldFromActorV1(ctx, args, nil)
}
func (s *Supervisor) resolveNetworkHoldFromActorV1(ctx context.Context, args api.NetworkHoldActionArgs, expectedActor *egressProc) (api.NetworkHoldResolution, *proto.Error) {
	var empty api.NetworkHoldResolution
	s.polMu.Lock()
	defer s.polMu.Unlock()
	actor := s.liveNetworkActor()
	if actor == nil || expectedActor != nil && actor != expectedActor {
		return empty, endedNetworkRequest()
	}
	var raw json.RawMessage
	if err := actor.ctl.request(ctx, egress.CtlNetworkResolve, args, &raw); err != nil {
		return empty, toProtoErr(err)
	}
	if !s.sameNetworkActor(actor) {
		return empty, endedNetworkRequest()
	}
	var resolved api.NetworkHoldResolution
	if !api.ExactJSONFields(raw, resolved) || json.Unmarshal(raw, &resolved) != nil {
		return empty, invalidNetworkResolutionV1()
	}
	selected, ok := validateResolvedNetworkSnapshotV1(resolved, args, time.Now())
	if !ok {
		return empty, invalidNetworkResolutionV1()
	}
	if args.Scope != api.NetworkHoldSession {
		return resolved, nil
	}
	s.mu.Lock()
	before := api.CloneLayers(s.m.Layers)
	pending := slices.Clone(s.m.PolicyNext)
	ref := sessionRefOf(s.m)
	s.mu.Unlock()
	restriction := args.Maximum == api.NetworkHoldPublicOnly || selected.Prefix.IsValid()
	policy, layers, rule, err := host.ApplyPolicyAdd(before, api.PolicyAddArgs{Allow: true, Door: api.DoorNet, Target: resolved.Target, PublicOnly: restriction})
	if err != nil {
		return empty, toProtoErr(err)
	}
	sources, err := api.DesiredPolicyLayersV1(layers, pending)
	if err != nil {
		return empty, toProtoErr(err)
	}
	expected, err := api.HashPolicyLaptopLayersV1(sources)
	if err != nil {
		return empty, toProtoErr(err)
	}
	if !s.sameNetworkActor(actor) || !time.Now().Before(resolved.Expires) {
		return empty, endedNetworkRequest()
	}
	if err := pushReplacementToEgress(ctx, actor, ref, policy, sources, expected); err != nil {
		return empty, toProtoErr(err)
	}
	s.saveMu.Lock()
	var snapshot host.Manifest
	change := func(current *host.Manifest) error {
		if !s.sameNetworkActor(actor) || !time.Now().Before(resolved.Expires) || !reflect.DeepEqual(current.Layers, before) || !bytes.Equal(current.PolicyNext, pending) {
			return endedNetworkRequest()
		}
		current.Policy = &policy
		current.Layers = api.CloneLayers(layers)
		return nil
	}
	publish := func(current *host.Manifest) {
		pol := api.ClonePolicy(*current.Policy)
		s.m.Policy = &pol
		s.m.Layers = api.CloneLayers(current.Layers)
	}
	err = s.writeManifestChange(&snapshot, false, nil, nil, change, publish)
	s.saveMu.Unlock()
	if err != nil {
		s.failPolicySave(actor)
		return empty, toProtoErr(err)
	}
	if err := s.reconcilePorts(); err != nil {
		return empty, toProtoErr(err)
	}
	s.audit.write("network_request_session", map[string]any{"request": args.Request, "rule": rule.ID, "target": rule.Target, "public_only": rule.PublicOnly})
	return resolved, nil
}
func invalidNetworkResolutionV1() *proto.Error {
	return &proto.Error{Code: "network_snapshot", Message: "the original actor returned an invalid or changed pending resolution", Hint: "Refresh the original request with matching helpers before answering."}
}

// Parsing here validates a completed non-sensitive private actor snapshot's
// representation. It never derives a target from caller/display metadata.
func validateResolvedNetworkSnapshotV1(resolved api.NetworkHoldResolution, args api.NetworkHoldActionArgs, now time.Time) (nettarget.Target, bool) {
	if !api.ValidNetworkHoldPositiveAction(args) || args.Scope == api.NetworkHoldOnce || resolved.Version != api.NetworkHoldV1 || resolved.Request != args.Request || resolved.Scope != args.Scope || resolved.Revision != args.Revision || resolved.Class != api.NetworkHoldPublicTarget || resolved.Expires.IsZero() || !now.Before(resolved.Expires) || len(resolved.Target) > 512 || api.CleanText(resolved.Target, len(resolved.Target)) != resolved.Target {
		return nettarget.Target{}, false
	}
	target, err := nettarget.Parse(resolved.Target)
	if err != nil || target.Wildcard || target.Path != "" || target.Port < 1 || target.Port > 65535 || target.String() != resolved.Target || target.Prefix.IsValid() && target.Prefix.Bits() != target.Prefix.Addr().BitLen() {
		return nettarget.Target{}, false
	}
	return target, true
}
