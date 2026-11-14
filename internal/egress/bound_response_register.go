package egress

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

func (s *Server) registerResponseSecret(ctx context.Context, authority string, use secretRequestUse, value api.Secret, ruleID string) (api.Secret, error) {
	if err := sentinelContext(ctx, "registry"); err != nil {
		return "", err
	}
	// Publication uses the existing control operation lifetime, including
	// durable acknowledgement. An earlier response deadline still wins.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.policyMu.LockContext(ctx); err != nil {
		return "", err
	}
	reservation, err := s.reserveResponseSecretLocked(ctx, authority, use, value, ruleID)
	s.policyMu.Unlock()
	if err != nil {
		return "", err
	}
	return s.finishMintReservation(ctx, reservation, use)
}

func (s *Server) reserveResponseSecretLocked(ctx context.Context, authority string, use secretRequestUse, value api.Secret, ruleID string) (*mintReservation, error) {
	current := s.currentMembers()
	if current == nil || current.version != MemberRegistryV2 || !validBoundCredential(value) {
		return nil, errResponseRegistry
	}
	name, err := responseMintOwner(current, authority, use)
	if err != nil {
		return nil, err
	}
	owner := current.secrets[name]
	for _, existingName := range slices.Sorted(maps.Keys(current.secrets)) {
		secret := current.secrets[existingName]
		if secret.identity != owner.identity || secret.bound == nil {
			continue
		}
		if secret.present && secret.value == value || slices.Contains(secret.history, value) {
			return &mintReservation{name: name, epoch: owner.valueEpoch, parent: owner.identity, identity: owner.identity, authority: authority, sentinel: owner.bound.sentinel}, nil
		}
		for i, mint := range secret.minted {
			if !mint.retired && mint.authority == authority && mint.value == value {
				if mint.ready {
					return reserveExistingMint(ctx, current, existingName, mint, use)
				}
				if mint.publication == nil {
					return nil, secretSaveFailure()
				}
				finished, _ := mint.publication.result()
				if finished || mint.publication.epoch != secret.valueEpoch {
					return s.retryMintPublicationLocked(ctx, current, existingName, i, use)
				}
				return reserveExistingMint(ctx, current, existingName, mint, use)
			}
		}
	}
	if registrySentinels(current)[value] {
		return nil, registryCollisionError(name)
	}
	pattern, err := secretRuleExpression(ruleID)
	if err != nil || pattern == "" {
		return nil, errResponseRegistry
	}
	if err := checkMintedHTTPValue(name, owner.bound.binding, value); err != nil {
		return nil, err
	}
	shape, err := compileSentinelFormat(ctx, name, value, "", pattern, bindingPunctuationPolicy(owner.bound.binding))
	if err != nil {
		return nil, err
	}
	next, err := cloneMemberState(current)
	if err != nil {
		return nil, err
	}
	reserved := registryReservedValues(next)
	reserved[value] = true
	sentinel, err := makeSentinel(ctx, name, &value, shape, func(candidate api.Secret) bool { return !reserved[candidate] })
	if err != nil {
		return nil, err
	}
	identity, err := registryNonce()
	if err != nil {
		return nil, err
	}
	// Additive variants replace immutable views without changing the
	// primary/membership authority CAS used by an approved arm operation.
	mint := registeredMint{value: value, sentinel: sentinel, shape: shape, rule: ruleID, authority: authority, identity: identity}
	mint.publication, err = newMintPublication(next, name, mint)
	if err != nil {
		return nil, err
	}
	owner = next.secrets[name]
	owner.minted = append(owner.minted, mint)
	next.secrets[name] = owner
	return s.publishMintReservation(ctx, next, name, mint, use)
}

func checkMintedHTTPValue(name string, binding api.SecretBind, value api.Secret) error {
	kind, user, err := secretbind.ParseFormat(binding.Format)
	if err != nil {
		return registryIntentError()
	}
	if field, valid := requestCredentialValue(kind, user, value); !valid || len(field)+len(binding.Header)+4 > maxHTTPHeaderBytes {
		return memberError("secret_format", "credential cannot use secret "+name+"'s approved header format", "Use a separately approved matching binding on this exact authority.")
	}
	return nil
}

func reserveExistingMint(ctx context.Context, state *memberState, name string, mint registeredMint, use secretRequestUse) (*mintReservation, error) {
	owner := state.secrets[name]
	reservation := &mintReservation{name: name, epoch: owner.valueEpoch, parent: owner.identity, identity: mint.identity, authority: mint.authority, sentinel: mint.sentinel}
	if !mint.ready {
		if mint.publication == nil || mint.retired {
			return nil, secretSaveFailure()
		}
		public, err := publicMintedState(ctx, name, mint)
		if err != nil {
			return nil, err
		}
		p := mint.publication
		reservation.publication = p
		reservation.event = MintedStateEvent{Version: MemberRegistryV2, Generation: p.generation, Receipt: p.receipt, Owner: name, State: public}
		reservation.waiter = p.addWaiter(ctx, use)
		if reservation.waiter == nil {
			return nil, secretSaveFailure()
		}
	}
	return reservation, nil
}

func (s *Server) publishMintReservation(ctx context.Context, next *memberState, name string, mint registeredMint, use secretRequestUse) (*mintReservation, error) {
	prepared, err := prepareBoundRegistry(ctx, next)
	if err != nil {
		return nil, err
	}
	prepared.secretOnly = true
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reservation, err := reserveExistingMint(ctx, next, name, mint, use)
	if err != nil {
		return nil, err
	}
	s.publishControlState(prepared, false)
	return reservation, nil
}

func (s *Server) retryMintPublicationLocked(ctx context.Context, current *memberState, name string, index int, use secretRequestUse) (*mintReservation, error) {
	next, err := cloneMemberState(current)
	if err != nil {
		return nil, err
	}
	owner := next.secrets[name]
	mint := owner.minted[index]
	mint.publication, err = newMintPublication(next, name, mint)
	if err != nil {
		return nil, err
	}
	owner.minted[index] = mint
	next.secrets[name] = owner
	return s.publishMintReservation(ctx, next, name, mint, use)
}
