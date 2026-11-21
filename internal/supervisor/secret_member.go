package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) installMemberPolicy(ctx context.Context, child *host.Manifest, approved map[string]api.Secret, intent *api.SecretDeliveryIntent, expectedGeneration string, protectedTrusted ...map[string]api.Secret) (secretEnvironment, error) {
	root := s.owner()
	if root.unwalledV1() {
		if len(protectedTrusted) > 1 || len(protectedTrusted) == 1 && len(protectedTrusted[0]) > 0 {
			return secretEnvironment{}, secretIntentFailure()
		}
		return root.installNoneMemberV1(ctx, child, approved, intent, expectedGeneration)
	}
	if len(protectedTrusted) > 1 {
		return secretEnvironment{}, secretIntentFailure()
	}
	if len(protectedTrusted) == 1 && len(protectedTrusted[0]) > 0 && intent == nil {
		return secretEnvironment{}, secretIntentFailure()
	}
	root.mu.Lock()
	modern := root.m.SecretState != nil || child.SecretState != nil || intent != nil
	root.mu.Unlock()
	if !modern {
		env, err := root.installLegacyMemberPolicy(ctx, child, approved)
		if err != nil {
			return secretEnvironment{}, err
		}
		result := secretEnvironment{Env: env}
		for _, name := range child.Secrets {
			if _, present := approved[name]; !present {
				result.Missing = append(result.Missing, name)
			}
		}
		return result, nil
	}
	return root.installBoundMemberPolicy(ctx, child, approved, intent, expectedGeneration, protectedTrusted...)
}

func (s *Supervisor) installBoundMemberPolicy(ctx context.Context, child *host.Manifest, approved map[string]api.Secret, intent *api.SecretDeliveryIntent, expectedGeneration string, protectedTrusted ...map[string]api.Secret) (secretEnvironment, error) {
	if intent == nil {
		return secretEnvironment{}, secretIntentFailure()
	}
	trustedValues, err := memberTrustedValues(protectedTrusted)
	if err != nil {
		return secretEnvironment{}, err
	}
	defer clear(trustedValues)
	if err := validateProtectedTrustedValues(intent.TrustedUses, trustedValues, true); err != nil {
		return secretEnvironment{}, err
	}

	owned, requested, err := host.PrepareSecretDelivery(intent, approved, true, recordedSecretNames(child.Secrets))
	if err != nil {
		return secretEnvironment{}, err
	}
	for name, mode := range owned.Modes {
		if _, supplied := approved[name]; mode == api.SecretModeBound && !supplied {
			return secretEnvironment{}, missingBoundShape()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, secretControlWait)
	defer cancel()
	s.polMu.Lock()
	defer s.polMu.Unlock()
	s.mu.Lock()
	owner := s.eg
	snapshot := s.snapshot()
	ready := s.rec.phase == host.LifeRunning && !s.memberFault && ownedSecretControl(s, owner)
	s.mu.Unlock()
	if !ready || snapshot.SecretState == nil || !snapshot.SecretState.Supported() {
		return secretEnvironment{}, secretNextStartAuthority()
	}
	if expectedGeneration != "" && expectedGeneration != snapshot.SecretState.Generation {
		return secretEnvironment{}, secretIntentFailure()
	}
	if (len(approved) > 0 || len(trustedValues) > 0) && expectedGeneration == "" {
		return secretEnvironment{}, secretIntentFailure()
	}
	if err := requireBoundControl(ctx, owner); err != nil {
		return secretEnvironment{}, err
	}
	if err := requireTrustedJudgeControl(ctx, owner, owned.TrustedUses); err != nil {
		return secretEnvironment{}, err
	}
	if err := s.requireBoundGuest(ctx); err != nil {
		return secretEnvironment{}, err
	}
	values := maps.Clone(approved)
	defer clear(values)
	authorizedGeneration := expectedGeneration
	if authorizedGeneration == "" {
		authorizedGeneration = snapshot.SecretState.Generation
	} // value-free trusted internal join only
	args := egress.MemberAddV2Args{Generation: authorizedGeneration, Version: egress.MemberRegistryV2, Kind: selectedSecretKind(child.Agent.Kind), Values: values,
		Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind), TrustedUses: owned.TrustedUses, ProtectedTrustedSecretValues: trustedValues}
	for name := range values {
		mode := owned.Modes[name]
		if previous, exists := snapshot.SecretState.Intent.Modes[name]; exists && (previous != mode || mode == api.SecretModeBound && !sameSecretBinding(snapshot.SecretState.Intent.Binds[name], owned.Binds[name])) {
			return secretEnvironment{}, secretNextStartAuthority()
		}
		args.Modes[name] = mode
		if mode == api.SecretModeBound {
			args.Binds[name] = owned.Binds[name]
		}
	}
	finishKind := owner.ctl.beginPublicKind(args.Kind)
	retainKind := false
	defer func() { finishKind(retainKind) }()
	var raw json.RawMessage
	if err := owner.ctl.request(ctx, egress.CtlMemberAdd, args, &raw); err != nil {
		var negative *proto.Error
		if errors.As(err, &negative) {
			return secretEnvironment{}, &memberPolicyFailure{cause: negative}
		}
		retainKind = true
		owner.ctl.closeOwnedControl()
		return secretEnvironment{}, &memberPolicyFailure{preserve: true, cause: secretCommitFailure()}
	}
	retainKind = true
	var reply egress.MemberAddResultV2
	if !api.ExactJSONFields(raw, reply) || json.Unmarshal(raw, &reply) != nil || validateMemberSecretReply(ctx, args, reply, snapshot.SecretState) != nil {
		owner.ctl.closeOwnedControl()
		return secretEnvironment{}, &memberPolicyFailure{preserve: true, cause: secretCommitFailure()}
	}
	generation := snapshot.SecretState.Generation
	state, err := s.commitSecretPublicState(ctx, owner, &generation, func(current *api.SessionSecretState) (*api.SessionSecretState, error) {
		if current == nil || !current.Supported() {
			return nil, secretCommitFailure()
		}
		initialiseSavedSecretMaps(current)
		for name, public := range reply.Secrets {
			if previous, exists := current.Primary[name]; exists && !sameLivePrimary(previous, public) {
				return nil, secretCommitFailure()
			}
			current.Intent.Modes[name] = args.Modes[name]
			if args.Modes[name] == api.SecretModeBound {
				current.Intent.Binds[name] = args.Binds[name]
			}
			current.Primary[name] = public
		}
		current.Intent.TrustedUses = api.CloneTrustedSecretUses(reply.TrustedUses)
		current.Generation = reply.Generation
		return current, nil
	})
	if err != nil {
		owner.ctl.closeOwnedControl()
		return secretEnvironment{}, &memberPolicyFailure{preserve: true, cause: secretCommitFailure()}
	}
	actual := make(map[string]string, len(approved))
	for name, value := range approved {
		actual[name] = string(value)
	}
	names := slices.Sorted(maps.Keys(approved))
	environment, err := secretEnvironmentFor(names, *owned, state.Primary, actual)
	clear(actual)
	if err != nil {
		owner.ctl.closeOwnedControl()
		return secretEnvironment{}, &memberPolicyFailure{preserve: true, cause: secretCommitFailure()}
	}
	for _, name := range requested {
		if _, supplied := approved[name]; !supplied {
			environment.Missing = append(environment.Missing, name)
		}
	}
	slices.Sort(environment.Missing)
	s.audit.write("member_add", map[string]any{"session": child.ID, "agent": args.Kind, "secrets": names})
	return environment, nil
}

func validateMemberSecretReply(ctx context.Context, args egress.MemberAddV2Args, reply egress.MemberAddResultV2, current *api.SessionSecretState) error {
	if reply.Version != egress.MemberRegistryV2 || len(reply.Env) != len(args.Values) || len(reply.Secrets) != len(args.Values) || !egress.EqualTrustedSecretUsesV1(overlayTrustedUses(current.Intent.TrustedUses, args.TrustedUses), reply.TrustedUses) {
		return secretCommitFailure()
	}
	actuals := make(map[api.Secret]bool, len(args.Values))
	for _, value := range args.Values {
		actuals[value] = true
	}
	for name, value := range args.Values {
		public, present := reply.Secrets[name]
		env, included := reply.Env[name]
		if !present || !included || public.Mode != args.Modes[name] || !public.Armed {
			return secretCommitFailure()
		}
		if previous, exists := current.Primary[name]; exists && !sameLivePrimary(previous, public) {
			return secretCommitFailure()
		}
		if public.Mode == api.SecretModeBound {
			if env != public.Sentinel || actuals[public.Sentinel] || egress.ValidatePublicSecretValue(ctx, name, public, args.Binds[name], value) != nil {
				return secretCommitFailure()
			}
		} else if env != value {
			return secretCommitFailure()
		}
	}
	return nil
}
