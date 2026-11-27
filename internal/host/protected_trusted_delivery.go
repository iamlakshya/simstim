package host

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func (carrier *protectedTrustedStartupV1) checkSource(ctx context.Context, operation *SessionOperation) error {
	if ctx == nil || ctx.Err() != nil || operation == nil || carrier == nil || carrier.operation != operation || !operation.Valid(operation.Primary()) {
		return protectedTrustedStartErrorV1()
	}
	if carrier.cold != nil {
		p := carrier.cold
		if p.operation != operation || !p.active.Load() || !p.consumed || p.requestID == 0 || len(p.finalEncoded) == 0 || p.cold == nil || p.bridge == nil || p.bridge.ctx.Err() != nil {
			return protectedTrustedStartErrorV1()
		}
		return p.cold.Check(ctx, operation)
	}
	witness := carrier.newRequest
	if witness == nil {
		return protectedTrustedStartErrorV1()
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.bridge == nil || witness.bridge.ctx == nil || witness.bridge.ctx.Err() != nil || witness.requestID == 0 || !witness.captured || witness.bound != operation || len(witness.raw) == 0 || len(witness.encoded) == 0 {
		return protectedTrustedStartErrorV1()
	}
	if operation.newRequest != nil && (operation.newRequest != witness || !witness.written) {
		return protectedTrustedStartErrorV1()
	}
	return nil
}

// takeProtectedTrustedSecretsV1 belongs only to the original native sender.
// The typed member inputs must match the captured roles and private maps;
// no caller map is adopted as an alternative protected source.
func (operation *SessionOperation) takeProtectedTrustedSecretsV1(ctx context.Context, intent *api.SecretDeliveryIntent, members []api.MemberResumeArgs) (map[string]api.Secret, []api.MemberResumeArgs, error) {
	if operation == nil || ctx == nil || ctx.Err() != nil || !operation.Valid(operation.Primary()) {
		return nil, nil, protectedTrustedStartErrorV1()
	}
	operation.gate.Lock()
	carrier := operation.protectedTrustedStart
	consumed := operation.protectedTrustedConsumed
	operation.gate.Unlock()
	if consumed {
		return nil, nil, protectedTrustedStartErrorV1()
	}
	if carrier == nil {
		// An automatic cold start may retain validated public roles unarmed.
		// A raw member private map never supplies a replacement carrier.
		for _, member := range members {
			if len(member.ProtectedTrustedSecretValues) > 0 {
				return nil, nil, protectedTrustedStartErrorV1()
			}
		}
		return nil, members, nil
	}
	if err := carrier.checkSource(ctx, operation); err != nil {
		return nil, nil, err
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.protectedTrustedStart != carrier || operation.protectedTrustedConsumed {
		return nil, nil, protectedTrustedStartErrorV1()
	}
	var uses map[string]api.TrustedSecretUseV1
	if intent != nil {
		uses = intent.TrustedUses
	}
	if !egress.EqualTrustedSecretUsesV1(uses, carrier.primary.Uses) || len(members) != len(carrier.members) {
		return nil, nil, protectedTrustedStartErrorV1()
	}
	selected := slices.Clone(members)
	seen := make(map[string]bool, len(selected))
	for index, member := range selected {
		captured, exists := carrier.members[member.ID]
		var requested map[string]api.TrustedSecretUseV1
		if member.SecretDelivery != nil {
			requested = member.SecretDelivery.TrustedUses
		}
		if !exists || seen[member.ID] || !egress.EqualTrustedSecretUsesV1(requested, captured.Uses) || !maps.Equal(member.ProtectedTrustedSecretValues, captured.Values) {
			return nil, nil, protectedTrustedStartErrorV1()
		}
		seen[member.ID] = true
		selected[index].ProtectedTrustedSecretValues = captured.Values
	}
	operation.protectedTrustedConsumed = true
	primary := carrier.primary.Values
	carrier.primary.Values = nil
	for id, process := range carrier.members {
		process.Values = nil
		carrier.members[id] = process
	}
	return primary, selected, nil
}

func (operation *SessionOperation) clearProtectedTrustedSecretsV1() {
	operation.gate.Lock()
	carrier := operation.protectedTrustedStart
	operation.protectedTrustedStart = nil
	if carrier != nil {
		carrier.clearValues()
	}
	operation.gate.Unlock()
	if carrier != nil && carrier.newRequest != nil {
		witness := carrier.newRequest
		witness.mu.Lock()
		clear(witness.raw)
		clear(witness.encoded)
		witness.raw, witness.encoded = nil, nil
		witness.mu.Unlock()
	}
}

func clearProtectedTrustedMembersV1(members []api.MemberResumeArgs) {
	for index := range members {
		clear(members[index].ProtectedTrustedSecretValues)
		members[index].ProtectedTrustedSecretValues = nil
	}
}
