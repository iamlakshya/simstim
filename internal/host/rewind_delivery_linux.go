//go:build linux

package host

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func (p *bridgeRewindPreparationV1) captureStartupDeliveryV1(ctx context.Context, cold *ColdResumeV1, m *Manifest, args api.ResumeArgs) error {
	if p == nil || p.operation == nil || p.cold != cold || p.accounting == nil || m == nil || m.Policy == nil || m.ID != p.operation.Primary() || p.phase.Load() != rewindRestartingV1 {
		return protectedTrustedStartErrorV1()
	}
	if err := errors.Join(p.checkCommittedV1(ctx, p.operation, cold, args), p.accounting.Check(ctx, p.operation, cold), cold.CheckManifest(ctx, p.operation, m)); err != nil {
		return err
	}
	previous := p.startupDelivery
	if previous != nil {
		if previous.preparation != p || previous.cold == cold || p.retry == nil || p.retry.previousColdV1() != previous.cold {
			return protectedTrustedStartErrorV1()
		}
		if err := p.retry.checkV1(ctx, p.operation); err != nil {
			return err
		}
	}
	if err := checkNewNamedSSHSelection(*m.Policy, args.NamedSSHKeys); err != nil {
		return err
	}
	if args.NamedSSHKeys != nil {
		for _, name := range args.NamedSSHKeys.Names {
			if _, ordinary := args.Secrets[name]; ordinary {
				return namedSSHStartError("an SSH key cannot enter ordinary rewind secret delivery")
			}
			for _, member := range args.Members {
				if _, ordinary := member.Secrets[name]; ordinary {
					return namedSSHStartError("an SSH key cannot enter a member's ordinary rewind secret delivery")
				}
			}
		}
	}
	delivery := &rewindStartupDeliveryV1{preparation: p, cold: cold, previous: previous, retry: p.retry, named: api.CloneNamedSSHKeysV1(args.NamedSSHKeys), members: make(map[string]protectedTrustedProcessV1, len(args.Members))}
	keep := false
	defer func() {
		if !keep {
			// A failed candidate must not clear the previous actual carrier.
			delivery.previous = nil
			delivery.clearV1()
		}
	}()
	var err error
	delivery.primary, err = captureProtectedTrustedProcessV1(args.SecretDelivery, args.ProtectedTrustedSecretValues)
	if err != nil {
		return err
	}
	for _, member := range args.Members {
		if !api.ValidSessionID(member.ID) || member.ID == m.ID {
			return protectedTrustedStartErrorV1()
		}
		if _, duplicate := delivery.members[member.ID]; duplicate {
			return protectedTrustedStartErrorV1()
		}
		process, err := captureProtectedTrustedProcessV1(member.SecretDelivery, member.ProtectedTrustedSecretValues)
		if err != nil {
			return err
		}
		delivery.members[member.ID] = process
	}
	if p.startupDelivery != previous {
		return protectedTrustedStartErrorV1()
	}
	p.startupDelivery = delivery
	keep = true
	return nil
}

// The actual startup sender holds launch.mu. Public value maps, a retry
// count, an ACK and the saved journal cannot enter this private take path.
func (p *bridgeRewindPreparationV1) takeStartupDeliveryV1(ctx context.Context, launch *startupLaunchV1, intent *api.SecretDeliveryIntent, members []api.MemberResumeArgs) (*api.NamedSSHKeysV1, map[string]api.Secret, []api.MemberResumeArgs, error) {
	if p == nil || launch == nil || p.operation == nil || launch.operation != p.operation || p.cold == nil || p.cold.startup != launch || p.operation.startup != launch || p.operation.nativeColdResume != p.cold || !launch.loanSent || launch.ready || p.accounting == nil || p.phase.Load() != rewindRestartingV1 {
		return nil, nil, nil, protectedTrustedStartErrorV1()
	}
	if err := errors.Join(p.checkCommittedV1(ctx, p.operation, p.cold, p.fresh), p.accounting.Check(ctx, p.operation, p.cold), launch.check(ctx)); err != nil {
		return nil, nil, nil, err
	}
	delivery := p.startupDelivery
	if delivery == nil {
		return nil, nil, nil, protectedTrustedStartErrorV1()
	}
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	if delivery.preparation != p || delivery.cold != p.cold || delivery.closed || delivery.consumed || delivery.retry != p.retry {
		return nil, nil, nil, protectedTrustedStartErrorV1()
	}
	var uses map[string]api.TrustedSecretUseV1
	if intent != nil {
		uses = intent.TrustedUses
	}
	if !egress.EqualTrustedSecretUsesV1(uses, delivery.primary.Uses) || len(members) != len(delivery.members) {
		return nil, nil, nil, protectedTrustedStartErrorV1()
	}
	selected := slices.Clone(members)
	seen := make(map[string]bool, len(selected))
	for i, member := range selected {
		process, exists := delivery.members[member.ID]
		var requested map[string]api.TrustedSecretUseV1
		if member.SecretDelivery != nil {
			requested = member.SecretDelivery.TrustedUses
		}
		if !exists || seen[member.ID] || !egress.EqualTrustedSecretUsesV1(requested, process.Uses) || !maps.Equal(member.ProtectedTrustedSecretValues, process.Values) {
			return nil, nil, nil, protectedTrustedStartErrorV1()
		}
		seen[member.ID] = true
		selected[i].ProtectedTrustedSecretValues = process.Values
	}
	named, primary := delivery.named, delivery.primary.Values
	delivery.named, delivery.primary.Values = nil, nil
	for id, process := range delivery.members {
		process.Values = nil
		delivery.members[id] = process
	}
	delivery.consumed = true
	return named, primary, selected, nil
}
