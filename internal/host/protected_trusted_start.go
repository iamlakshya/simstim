package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

type protectedTrustedProcessV1 struct {
	Uses   map[string]api.TrustedSecretUseV1
	Values map[string]api.Secret
}

// This private carrier belongs to one original native startup. It holds no
// guest environment/name set and cannot issue a source from public metadata.
type protectedTrustedStartupV1 struct {
	operation  *SessionOperation
	newRequest *newRequestWitnessV1
	cold       *bridgeResumePreparationV1
	primary    protectedTrustedProcessV1
	members    map[string]protectedTrustedProcessV1
}

func protectedTrustedStartErrorV1() error {
	return UserError("secret_delivery", "the protected classifier credential lost its original native start source", "Use the same verified client request and its separately approved trusted credential role.")
}

func captureProtectedTrustedProcessV1(intent *api.SecretDeliveryIntent, values map[string]api.Secret) (protectedTrustedProcessV1, error) {
	if _, err := checkSecrets(values); err != nil {
		return protectedTrustedProcessV1{}, err
	}
	var uses map[string]api.TrustedSecretUseV1
	if intent != nil {
		uses = intent.TrustedUses
	}
	for name, use := range uses {
		if err := egress.ValidateTrustedSecretUseV1(name, use); err != nil {
			return protectedTrustedProcessV1{}, err
		}
	}
	for name := range values {
		if _, present := uses[name]; !present {
			return protectedTrustedProcessV1{}, protectedTrustedStartErrorV1()
		}
	}
	return protectedTrustedProcessV1{Uses: api.CloneTrustedSecretUses(uses), Values: maps.Clone(values)}, nil
}

func hasProtectedTrustedStartupV1(intent *api.SecretDeliveryIntent, values map[string]api.Secret, members []api.MemberResumeArgs) bool {
	if len(values) > 0 || intent != nil && len(intent.TrustedUses) > 0 {
		return true
	}
	for _, member := range members {
		if len(member.ProtectedTrustedSecretValues) > 0 || member.SecretDelivery != nil && len(member.SecretDelivery.TrustedUses) > 0 {
			return true
		}
	}
	return false
}

// captureNewProtectedTrustedSecretsV1 consumes the actual captured bridge New,
// its original reserved OP/root and unchanged typed role declaration.
func captureNewProtectedTrustedSecretsV1(ctx context.Context, operation *SessionOperation, m *Manifest, original NewArgs) error {
	if !hasProtectedTrustedStartupV1(original.SecretDelivery, original.ProtectedTrustedSecretValues, nil) {
		return nil
	}
	if ctx == nil || ctx.Err() != nil || operation == nil || operation.native == nil || operation.native.borrowed || m == nil || m.Wall == nil || m.Wall.Driver == "plain" || original.ID != m.ID || m.SandboxOf != "" || !operation.Valid(m.ID) || m.SecretState == nil {
		return protectedTrustedStartErrorV1()
	}
	primary, err := captureProtectedTrustedProcessV1(original.SecretDelivery, original.ProtectedTrustedSecretValues)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			clear(primary.Values)
		}
	}()
	if !egress.EqualTrustedSecretUsesV1(primary.Uses, m.SecretState.Intent.TrustedUses) {
		return protectedTrustedStartErrorV1()
	}
	witness, _ := ctx.Value(newRequestKeyV1{}).(*newRequestWitnessV1)
	if witness == nil {
		return protectedTrustedStartErrorV1()
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		return err
	}
	defer clear(encoded)
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.bridge == nil || witness.bridge.ctx == nil || witness.bridge.ctx.Err() != nil || witness.requestID == 0 || !witness.captured || witness.written || witness.bound != nil && witness.bound != operation || len(witness.raw) == 0 || !bytes.Equal(encoded, witness.encoded) {
		return protectedTrustedStartErrorV1()
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.protectedTrustedStart != nil || operation.protectedTrustedConsumed {
		return protectedTrustedStartErrorV1()
	}
	operation.protectedTrustedStart = &protectedTrustedStartupV1{operation: operation, newRequest: witness, primary: primary}
	witness.bound = operation
	keep = true
	return nil
}

// Only the same real cold continuation may capture a complete root/member
// value bundle. The token, role declaration and Manifest alone grant nothing.
func captureResumeProtectedTrustedSecretsV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, m *Manifest, args api.ResumeArgs) error {
	if !hasProtectedTrustedStartupV1(args.SecretDelivery, args.ProtectedTrustedSecretValues, args.Members) {
		return nil
	}
	origin, err := requireResumePreparationAccountingOriginV1(ctx, operation)
	if err != nil {
		return err
	}
	if origin.rewind != nil {
		return protectedTrustedStartErrorV1()
	}
	p := origin.preparation
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	defer clear(encoded)
	if p.cold != cold || p.accounting == nil || !p.consumed || p.requestID == 0 || !bytes.Equal(encoded, p.finalEncoded) || !args.Prepared.Valid() || args.Prepared.Token != p.token || p.bridge == nil || p.bridge.ctx.Err() != nil || m == nil || m.Wall == nil || m.Wall.Driver == "plain" || m.ID != operation.Primary() {
		return protectedTrustedStartErrorV1()
	}
	if err := p.accounting.Check(ctx, operation, cold); err != nil {
		return err
	}
	carrier := &protectedTrustedStartupV1{operation: operation, cold: p, members: make(map[string]protectedTrustedProcessV1, len(args.Members))}
	keep := false
	defer func() {
		if !keep {
			carrier.clearValues()
		}
	}()
	carrier.primary, err = captureProtectedTrustedProcessV1(args.SecretDelivery, args.ProtectedTrustedSecretValues)
	if err != nil {
		return err
	}
	for _, member := range args.Members {
		if !api.ValidSessionID(member.ID) || member.ID == m.ID {
			return protectedTrustedStartErrorV1()
		}
		if _, exists := carrier.members[member.ID]; exists {
			return protectedTrustedStartErrorV1()
		}
		process, err := captureProtectedTrustedProcessV1(member.SecretDelivery, member.ProtectedTrustedSecretValues)
		if err != nil {
			return err
		}
		carrier.members[member.ID] = process
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.protectedTrustedStart != nil || operation.protectedTrustedConsumed {
		return protectedTrustedStartErrorV1()
	}
	operation.protectedTrustedStart = carrier
	keep = true
	return nil
}

func (carrier *protectedTrustedStartupV1) clearValues() {
	if carrier == nil {
		return
	}
	clear(carrier.primary.Values)
	for id, process := range carrier.members {
		clear(process.Values)
		delete(carrier.members, id)
	}
}

func (*protectedTrustedStartupV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
