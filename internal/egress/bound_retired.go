package egress

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

type registeredRetired struct {
	sentinel       api.Secret
	shape          sentinelShape
	identity, rule string
	binding        api.SecretBind
}

func (registeredRetired) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

// ValidateRetiredPrimarySecretState validates historical public metadata. It
// grants no current name, binding, inspection permission or destination grant.
func ValidateRetiredPrimarySecretState(ctx context.Context, name string, public api.RetiredPrimarySecretState) error {
	_, err := validatedRetiredPrimary(ctx, name, public)
	return err
}

func validatedRetiredPrimary(ctx context.Context, name string, public api.RetiredPrimarySecretState) (registeredRetired, error) {
	if err := checkRegistryValue(name, ""); err != nil {
		return registeredRetired{}, err
	}
	if !validRegistryNonce(public.Identity) || public.Shape == nil {
		return registeredRetired{}, invalidPublicSessionState()
	}
	// A historical binding validates syntax, not the current HTTP class or
	// network policy. Runtime inspection still fails closed if unsupported.
	binding, err := secretbind.Canonical(public.Binding)
	if err != nil {
		return registeredRetired{}, invalidPublicSessionState()
	}
	if err := checkSavedPrefix(name, public.Shape.Prefix); err != nil {
		return registeredRetired{}, err
	}
	shape, err := restoreSecretShape(ctx, name, api.PublicSecretState{Mode: api.SecretModeBound, Sentinel: public.Sentinel, Shape: public.Shape}, public.Shape.Prefix, bindingPunctuationPolicy(binding))
	if err != nil {
		return registeredRetired{}, err
	}
	plan, err := compileSentinelShape(ctx, name, shape)
	if err != nil || !plan.variable {
		return registeredRetired{}, invalidPublicSessionState()
	}
	if err := checkMintedHTTPValue(name, binding, public.Sentinel); err != nil {
		return registeredRetired{}, err
	}
	return registeredRetired{sentinel: public.Sentinel, shape: shape, identity: public.Identity, rule: public.Shape.Rule, binding: binding}, nil
}

func sameRetiredPrimary(a, b registeredRetired) bool {
	return a.sentinel == b.sentinel && a.identity == b.identity && a.rule == b.rule && sameSecretShape(a.shape, b.shape) && sameRegistryBinding(a.binding, b.binding)
}

func appendRetiredPrimary(entries map[string][]registeredRetired, name string, entry registeredRetired) error {
	for _, old := range entries[name] {
		if old.sentinel == entry.sentinel || old.identity == entry.identity {
			if !sameRetiredPrimary(old, entry) {
				return invalidPublicSessionState()
			}
			return nil
		}
	}
	entries[name] = append(entries[name], entry)
	return nil
}

func prepareRetiredPrimaries(ctx context.Context, saved map[string]api.PublicSecretState, retired map[string][]api.RetiredPrimarySecretState, binds map[string]api.SecretBind) (map[string][]registeredRetired, error) {
	entries := make(map[string][]registeredRetired)
	for _, name := range slices.Sorted(maps.Keys(retired)) {
		for _, public := range retired[name] {
			entry, err := validatedRetiredPrimary(ctx, name, public)
			if err != nil {
				return nil, err
			}
			if err := appendRetiredPrimary(entries, name, entry); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(saved)) {
		public := saved[name]
		if public.Mode != api.SecretModeBound {
			continue
		}
		entry, err := validatedRetiredPrimary(ctx, name, api.RetiredPrimarySecretState{Sentinel: public.Sentinel, Shape: public.Shape, Identity: public.Identity, Binding: binds[name]})
		if err != nil {
			return nil, err
		}
		if err := appendRetiredPrimary(entries, name, entry); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func cloneRetiredPrimaries(input map[string][]registeredRetired) map[string][]registeredRetired {
	if input == nil {
		return nil
	}
	output := make(map[string][]registeredRetired, len(input))
	for name, entries := range input {
		owned := slices.Clone(entries)
		for i := range owned {
			owned[i].shape = cloneSentinelShape(owned[i].shape)
			owned[i].binding.Hosts = slices.Clone(owned[i].binding.Hosts)
		}
		output[name] = owned
	}
	return output
}

func publicRetiredPrimaries(ctx context.Context, entries map[string][]registeredRetired) (map[string][]api.RetiredPrimarySecretState, error) {
	output := make(map[string][]api.RetiredPrimarySecretState, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		for _, entry := range entries[name] {
			shape, err := publicSecretShape(ctx, name, entry.shape, entry.rule)
			if err != nil {
				return nil, err
			}
			binding := entry.binding
			binding.Hosts = slices.Clone(binding.Hosts)
			output[name] = append(output[name], api.RetiredPrimarySecretState{Sentinel: entry.sentinel, Shape: shape, Identity: entry.identity, Binding: binding})
		}
	}
	return output, nil
}
