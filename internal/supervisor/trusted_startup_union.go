package supervisor

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// The complete original root/member envelope calls this once per actual
// process. It merges only separately protected trusted-role input; guest
// names, values, environment and format provenance retain their own domain.
func (p *startupSecretInput) addTrustedProcess(ctx context.Context, intent api.SecretDeliveryIntent, protected, guestValues map[string]api.Secret) error {
	if ctx == nil || ctx.Err() != nil {
		return secretIntentFailure()
	}
	if err := validateProtectedTrustedValues(intent.TrustedUses, protected, false); err != nil {
		return err
	}
	if len(intent.TrustedUses) > 0 && p.Intent.TrustedUses == nil {
		p.Intent.TrustedUses = make(map[string]api.TrustedSecretUseV1)
	}
	for _, name := range slices.Sorted(maps.Keys(intent.TrustedUses)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		use := intent.TrustedUses[name]
		if old, exists := p.Intent.TrustedUses[name]; exists && !egress.EqualTrustedSecretUsesV1(map[string]api.TrustedSecretUseV1{name: old}, map[string]api.TrustedSecretUseV1{name: use}) {
			return secretIntentFailure()
		}
		p.Intent.TrustedUses[name] = api.CloneTrustedSecretUses(map[string]api.TrustedSecretUseV1{name: use})[name]
		value, present := protected[name]
		if !present {
			continue
		}
		if old, exists := p.ProtectedTrustedSecretValues[name]; exists && old != value {
			return secretIntentFailure()
		}
		if old, exists := p.Values[name]; exists && old != value {
			return secretIntentFailure()
		}
		if old, exists := guestValues[name]; exists && old != value {
			return secretIntentFailure()
		}
		if p.ProtectedTrustedSecretValues == nil {
			p.ProtectedTrustedSecretValues = make(map[string]api.Secret)
		}
		p.ProtectedTrustedSecretValues[name] = value
	}
	return nil
}
