package supervisor

import (
	"bytes"
	"context"

	"github.com/iamlakshya/simstim/internal/host"
)

type originalSecretPayloadKeyV1 struct{}
type originalSecretPayloadV1 struct {
	startup   *originalStartup
	operation *host.SessionOperation
	raw       []byte
}

// Only the actual original native reader supplies this private payload. A
// normal prestart RPC never receives an alternative issuer or boolean grant.
func (s *Supervisor) validateOriginalStartupSecretsV1(ctx context.Context, start *originalStartup, raw []byte, want []string) (map[string]string, error) {
	if start == nil || start != s.originalStart || start.line == nil || s.bootOwner == nil || s.bootOperation == nil || !s.bootOperation.Valid(s.id) {
		return nil, secretIntentFailure()
	}
	if err := s.bootOwner.CheckStartupFrame(start.line); err != nil {
		return nil, err
	}
	witness := &originalSecretPayloadV1{startup: start, operation: s.bootOperation, raw: raw}
	owned := context.WithValue(ctx, originalSecretPayloadKeyV1{}, witness)
	return s.validateStartupSecrets(owned, raw, want)
}

func (s *Supervisor) checkOriginalPrivateStartupPayloadV1(ctx context.Context, raw []byte, message startupSecretEnvelope) error {
	protected := message.NamedSSHKeys != nil || len(message.ProtectedTrustedSecretValues) > 0 || message.SecretDelivery != nil && len(message.SecretDelivery.TrustedUses) > 0
	for _, member := range message.Members {
		protected = protected || len(member.ProtectedTrustedSecretValues) > 0 || member.SecretDelivery != nil && len(member.SecretDelivery.TrustedUses) > 0
	}
	if !protected {
		return nil
	}
	witness, _ := ctx.Value(originalSecretPayloadKeyV1{}).(*originalSecretPayloadV1)
	if witness == nil || witness.startup == nil || witness.startup != s.originalStart || witness.operation == nil || witness.operation != s.bootOperation || !witness.operation.Valid(s.id) || !bytes.Equal(witness.raw, raw) || s.m == nil || s.m.Wall == nil || s.m.Wall.Driver == "plain" || s.bootOwner == nil {
		return secretIntentFailure()
	}
	return s.bootOwner.CheckStartupFrame(witness.startup.line)
}
