package egress

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
)

func (s *Server) controlMutationContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if s.sess != nil {
		go func() {
			select {
			case <-s.sess.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

func (s *Server) initialiseBoundControl(raw []byte) (BoundInitResult, error) {
	ctx, cancel := s.controlMutationContext()
	defer cancel()
	if err := s.policyMu.LockContext(ctx); err != nil {
		return BoundInitResult{}, err
	}
	defer s.policyMu.Unlock()
	if s.isInited() {
		return BoundInitResult{}, memberError("bad_request", "egress is already initialised", "Use an approved arm or policy operation instead.")
	}
	var args BoundInitArgs
	if err := decodeBoundControlArgs(raw, &args); err != nil {
		return BoundInitResult{}, err
	}
	next, err := prepareBoundInitial(ctx, args)
	if err != nil {
		return BoundInitResult{}, err
	}
	minted, err := next.state.publicMinted(ctx)
	if err != nil {
		return BoundInitResult{}, err
	}
	retired, err := publicRetiredPrimaries(ctx, next.state.retired)
	if err != nil {
		return BoundInitResult{}, err
	}
	result := BoundInitResult{TrustedUses: next.state.publicTrustedUses(), Version: MemberRegistryV2, CACert: string(s.ca.publicPEM), Generation: next.state.generation, Secrets: next.state.publicSecrets(slices.Sorted(maps.Keys(next.state.secrets))), Minted: minted, Retired: retired}
	if err := checkBoundControlResult(result); err != nil {
		return BoundInitResult{}, err
	}
	s.publishControlState(next, true)
	return result, nil
}

func (s *Server) addVersionedControlMember(raw []byte) (any, error) {
	if err := uniqueControlJSON(raw); err != nil {
		return nil, err
	}
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return nil, registryControlError()
	}
	switch header.Version {
	case MemberRegistryV1:
		return s.addControlMember(raw)
	case MemberRegistryV2:
		return s.addBoundControlMember(raw)
	default:
		return nil, memberError("unsupported_member_registry", "unsupported member registry version", "Update both helpers before adding this member.")
	}
}

func (s *Server) addBoundControlMember(raw []byte) (MemberAddResultV2, error) {
	ctx, cancel := s.controlMutationContext()
	defer cancel()
	if err := s.policyMu.LockContext(ctx); err != nil {
		return MemberAddResultV2{}, err
	}
	var memberAudit *MemberAudit
	defer func() {
		if memberAudit != nil {
			s.emitLeaseEvent(CtlEvent{Event: EventMemberAdd, Member: memberAudit})
		}
	}()
	defer s.policyMu.Unlock()
	var args MemberAddV2Args
	if err := decodeBoundControlArgs(raw, &args); err != nil {
		return MemberAddResultV2{}, err
	}
	current := s.currentMembers()
	if current == nil {
		return MemberAddResultV2{}, memberError("bad_request", "egress is not initialised", "Initialise egress before adding a member.")
	}
	if current.version != MemberRegistryV2 || args.Version != MemberRegistryV2 {
		return MemberAddResultV2{}, memberError("unsupported_member_registry", "member addition requires the bound registry version", "Use matching bound-secret helpers for this session.")
	}
	if args.Generation == "" || args.Generation != current.generation {
		return MemberAddResultV2{}, memberError("secret_generation", "member authority generation changed; replacement did not occur", "Refresh the trusted session state and retry the freshly approved member operation.")
	}
	candidate, err := prepareBoundMember(ctx, current, args)
	if err != nil {
		return MemberAddResultV2{}, err
	}
	state := candidate.prepared.state
	result := MemberAddResultV2{TrustedUses: state.publicTrustedUses(), Version: MemberRegistryV2, Generation: state.generation, Env: candidate.env, Secrets: state.publicSecrets(candidate.names)}
	if err := checkBoundControlResult(result); err != nil {
		return MemberAddResultV2{}, err
	}
	if candidate.mutation {
		if err := s.prepareMemoryCarryLocked(ctx, candidate.prepared); err != nil {
			return MemberAddResultV2{}, err
		}
		if err := s.retireJudgeSourcesLocked(ctx, state); err != nil {
			return MemberAddResultV2{}, err
		}
		pending := append(secretAuthorities(current, candidate.changed), candidate.newAuthorities...)
		if err := s.closeSecretResources(ctx, candidate.changed, pending, candidate.newAuthorities); err != nil {
			return MemberAddResultV2{}, err
		}
		s.publishControlState(candidate.prepared, false)
	}
	memberAudit = &MemberAudit{Kind: args.Kind, Secrets: slices.Clone(candidate.names)}
	return result, nil
}

func (s *Server) armControl(raw []byte) (ArmResult, error) {
	ctx, cancel := s.controlMutationContext()
	defer cancel()
	if err := s.policyMu.LockContext(ctx); err != nil {
		return ArmResult{}, err
	}
	defer s.policyMu.Unlock()
	var args ArmArgs
	if err := decodeBoundControlArgs(raw, &args); err != nil {
		return ArmResult{}, err
	}
	current := s.currentMembers()
	if current == nil || current.version != MemberRegistryV2 || args.Version != MemberRegistryV2 {
		return ArmResult{}, memberError("unsupported_member_registry", "arm requires the bound registry version", "Use matching bound-secret helpers or resume explicitly.")
	}
	if args.Generation == "" || args.Generation != current.generation {
		return ArmResult{}, memberError("secret_generation", "secret generation changed; replacement did not occur", "Refresh the trusted session state and retry the freshly approved arm.")
	}
	next, err := cloneMemberState(current)
	if err != nil {
		return ArmResult{}, err
	}
	changed, err := applyTrustedArm(ctx, next, args)
	if err != nil {
		return ArmResult{}, err
	}
	if len(changed) == 0 {
		result := ArmResult{TrustedUses: current.publicTrustedUses(), Version: MemberRegistryV2, Generation: current.generation, Secrets: current.publicSecrets(slices.Sorted(maps.Keys(args.Values)))}
		if err := checkBoundControlResult(result); err != nil {
			return ArmResult{}, err
		}
		return result, nil
	}
	next.generation, err = registryNonce()
	if err != nil {
		return ArmResult{}, err
	}
	prepared, err := prepareBoundRegistry(ctx, next)
	if err != nil {
		return ArmResult{}, err
	}
	prepared.secretOnly = true
	result := ArmResult{TrustedUses: next.publicTrustedUses(), Version: MemberRegistryV2, Generation: next.generation, Secrets: next.publicSecrets(slices.Sorted(maps.Keys(args.Values)))}
	if err := checkBoundControlResult(result); err != nil {
		return ArmResult{}, err
	}
	if err := validateLiveBoundSentinels(current, next); err != nil {
		return ArmResult{}, err
	}
	if err := s.prepareMemoryCarryLocked(ctx, prepared); err != nil {
		return ArmResult{}, err
	}
	if err := s.retireJudgeSourcesLocked(ctx, next); err != nil {
		return ArmResult{}, err
	}
	addedAuthorities := trustedAddedAuthorities(current, next, changed)
	pendingAuthorities := append(secretAuthorities(current, changed), addedAuthorities...)
	if err := s.closeSecretResources(ctx, changed, pendingAuthorities, addedAuthorities); err != nil {
		return ArmResult{}, err
	}
	s.publishControlState(prepared, false)
	return result, nil
}

func secretAuthorities(state *memberState, names []string) []string {
	var authorities []string
	for _, name := range names {
		if secret, exists := state.secrets[name]; exists {
			if secret.bound != nil {
				authorities = append(authorities, secret.bound.binding.Hosts...)
			}
			if secret.trusted != nil {
				authorities = append(authorities, secret.trusted.Bind.Hosts...)
			}
		}
	}
	slices.Sort(authorities)
	return slices.Compact(authorities)
}
