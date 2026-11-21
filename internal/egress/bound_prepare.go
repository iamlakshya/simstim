package egress

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

func prepareBoundInitial(ctx context.Context, args BoundInitArgs) (*preparedInitial, error) {
	if err := sentinelContext(ctx, "registry"); err != nil {
		return nil, err
	}
	if args.Version != MemberRegistryV2 {
		return nil, memberError("unsupported_member_registry", "unsupported bound initialisation version", "Update both helpers before sending bound values.")
	}
	if err := checkBoundInitialFrame(args); err != nil {
		return nil, err
	}
	sources, prefixes, err := validateStartupSources(ctx, args)
	if err != nil {
		return nil, err
	}
	initial := args.Initial
	values := initial.Secrets
	initial.Secrets = nil
	base, err := prepareInitialState(initial)
	if err != nil {
		return nil, err
	}
	state := base.state
	state.version = MemberRegistryV2
	state.sourceKinds = sources
	for name := range values {
		if _, declared := args.Modes[name]; !declared {
			return nil, registryIntentError()
		}
	}
	for name := range args.Binds {
		if args.Modes[name] != api.SecretModeBound {
			return nil, registryIntentError()
		}
	}
	for name, saved := range args.Saved {
		if args.Modes[name] != api.SecretModeBound || saved.Mode != api.SecretModeBound {
			return nil, registryIntentError()
		}
	}
	for _, name := range slices.Sorted(maps.Keys(args.Modes)) {
		mode := args.Modes[name]
		value, present := values[name]
		if err := checkRegistryValue(name, api.Secret(value)); err != nil {
			return nil, err
		}
		entry := registeredSecret{mode: mode, value: api.Secret(value), present: present}
		switch mode {
		case api.SecretModeExposed:
			entry.identity = legacyExposedIdentity
		case api.SecretModeBound:
			binding, exists := args.Binds[name]
			if !exists {
				return nil, registryIntentError()
			}
			binding, err = canonicalRegistryBinding(name, binding)
			if err != nil {
				return nil, err
			}
			if saved, restoring := args.Saved[name]; restoring {
				entry.bound, err = restoredBoundSecret(ctx, name, saved, binding)
				if err == nil && present {
					err = checkSentinelValue(ctx, name, entry.value, entry.bound.shape)
					if err == nil {
						err = checkMintedHTTPValue(name, binding, entry.value)
					}
				}
			} else if present {
				entry.bound, err = publicBoundSecretWithPrefix(ctx, name, entry.value, binding, prefixes[name])
			} else {
				entry.bound, err = restoredBoundSecret(ctx, name, args.Saved[name], binding)
			}
			if err != nil {
				return nil, err
			}
		default:
			return nil, registryIntentError()
		}
		state.secrets[name] = entry
	}
	for name, kinds := range sources {
		if saved, restoring := args.Saved[name]; restoring {
			probe := state.secrets[name]
			public := *probe.bound
			public.sentinel = saved.Sentinel
			probe.bound = &public
			for _, kind := range kinds {
				if err := requireSelectedPrefix(kind, name, probe, probe.value, true); err != nil {
					return nil, err
				}
			}
		}
	}
	state.retired, err = prepareRetiredPrimaries(ctx, args.Saved, args.SavedRetired, args.Binds)
	if err != nil {
		return nil, err
	}
	state.generation, err = registryNonce()
	if err != nil {
		return nil, err
	}
	if err := restoreMintedState(ctx, state, args.SavedMinted); err != nil {
		return nil, err
	}
	if err := prepareTrustedInitial(ctx, state, args); err != nil {
		return nil, err
	}
	if err := assignInitialSentinels(ctx, state, args.Saved); err != nil {
		return nil, err
	}
	return prepareBoundRegistry(ctx, state)
}

func restoredBoundSecret(ctx context.Context, name string, saved api.PublicSecretState, binding api.SecretBind) (*boundSecret, error) {
	prefix := ""
	if saved.Shape != nil {
		prefix = saved.Shape.Prefix
	}
	if err := checkSavedPrefix(name, prefix); err != nil {
		return nil, err
	}
	shape, err := restoreSecretShape(ctx, name, saved, prefix, bindingPunctuationPolicy(binding))
	if err != nil {
		return nil, err
	}
	public, err := publicSecretShape(ctx, name, shape, saved.Shape.Rule)
	if err != nil {
		return nil, err
	}
	return &boundSecret{binding: binding, shape: shape, public: public, rule: saved.Shape.Rule}, nil
}

func registryIntentError() error {
	return memberError("bad_secret", "secret names, modes and bindings do not match the approved operation", "Retry with exact locally approved intent and matching helpers.")
}

func prepareBoundRegistry(ctx context.Context, state *memberState) (*preparedInitial, error) {
	if err := sentinelContext(ctx, "registry"); err != nil {
		return nil, err
	}
	metadata := state.metadata
	metadata.Secrets = nil
	metadata.AgentKinds = slices.Clone(state.kinds)
	metadata.Decoys = api.CloneDecoyTripwires(state.decoys)
	base, err := prepareInitialState(metadata)
	if err != nil {
		return nil, err
	}
	state.metadata, state.kinds = base.state.metadata, base.state.kinds
	state.decoys, state.decoyClasses = base.state.decoys, base.state.decoyClasses
	for name, secret := range state.secrets {
		if secret.bound != nil {
			if err := checkSavedPrefix(name, secret.bound.shape.Prefix); err != nil {
				return nil, err
			}
		}
	}
	if err := validateRegistryCollisions(ctx, state); err != nil {
		return nil, err
	}
	if err := checkBoundStateSize(ctx, state); err != nil {
		return nil, err
	}
	if err := checkBoundPublicStateSize(ctx, state); err != nil {
		return nil, err
	}
	redaction, err := registryRedaction(state)
	if err != nil {
		return nil, err
	}
	state.echo, err = registryResponseSnapshots(state)
	if err != nil {
		return nil, err
	}
	state.requestEntries, err = registryRequestEntries(state)
	if err != nil {
		return nil, err
	}
	state.requestView, err = newRequestSecretSnapshot(state.requestEntries)
	if err != nil {
		return nil, err
	}
	state.requestView, err = state.requestView.withTripwireSentinels(slices.Sorted(maps.Keys(registrySentinels(state))))
	if err != nil {
		return nil, err
	}
	state.requestView, err = state.requestView.withDecoyTripwires(state.decoys)
	if err != nil {
		return nil, err
	}
	base.state, base.redaction = state, redaction
	return base, nil
}
