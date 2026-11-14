package egress

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

type boundMemberCandidate struct {
	prepared                       *preparedInitial
	env                            map[string]api.Secret
	names, changed, newAuthorities []string
	mutation                       bool
}

func (boundMemberCandidate) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func prepareBoundMember(ctx context.Context, current *memberState, args MemberAddV2Args) (*boundMemberCandidate, error) {
	if args.Version != MemberRegistryV2 || len(args.Values) != len(args.Modes) {
		return nil, registryIntentError()
	}
	if args.Kind != "" {
		if _, err := canonicalAgentKinds([]string{args.Kind}); err != nil {
			return nil, err
		}
	}
	for name := range args.Modes {
		if _, present := args.Values[name]; !present {
			return nil, registryIntentError()
		}
	}
	for name := range args.Binds {
		if args.Modes[name] != api.SecretModeBound {
			return nil, registryIntentError()
		}
	}
	state, err := cloneMemberState(current)
	if err != nil {
		return nil, err
	}
	mutation := current.version != MemberRegistryV2
	if args.Kind != "" && !slices.Contains(state.kinds, args.Kind) {
		state.kinds = append(state.kinds, args.Kind)
		mutation = true
	}
	names := slices.Sorted(maps.Keys(args.Values))
	updates := make(map[string]api.Secret)
	var authorities []string
	for _, name := range names {
		value, mode := args.Values[name], args.Modes[name]
		if err := checkRegistryValue(name, value); err != nil {
			return nil, err
		}
		var binding api.SecretBind
		if mode == api.SecretModeBound {
			input, present := args.Binds[name]
			if !present {
				return nil, registryIntentError()
			}
			binding, err = canonicalRegistryBinding(name, input)
			if err != nil {
				return nil, err
			}
		} else if mode != api.SecretModeExposed {
			return nil, registryIntentError()
		}
		if old, exists := state.secrets[name]; exists {
			if old.mode != mode || mode == api.SecretModeBound && (old.bound == nil || !sameRegistryBinding(old.bound.binding, binding)) {
				return nil, memberError("secret_binding", "secret "+name+" changed mode or binding; replacement did not occur", "Start a fresh sandbox with the intended locally approved binding.")
			}
			if mode == api.SecretModeBound {
				if err := requireSelectedPrefix(args.Kind, name, old, value, false); err != nil {
					return nil, err
				}
				updates[name] = value
			} else {
				if old.present && old.value != value {
					return nil, memberError("secret_change_unsupported", "exposed secret "+name+" already has another running value", "Start or resume explicitly with the intended exposed values.")
				}
				if !old.present {
					old.value, old.present = value, true
					state.secrets[name], mutation = old, true
				}
			}
			continue
		}
		entry := registeredSecret{mode: mode, value: value, present: true, identity: legacyExposedIdentity}
		if mode == api.SecretModeBound {
			entry.bound, err = publicBoundSecret(ctx, args.Kind, name, value, binding)
			if err != nil {
				return nil, err
			}
			entry.identity = ""
			if state.sourceKinds == nil {
				state.sourceKinds = make(map[string][]string)
			}
			state.sourceKinds[name] = []string{args.Kind}
		}
		state.secrets[name], mutation = entry, true
	}
	changed, err := applyBoundUpdates(ctx, state, updates)
	if err != nil {
		return nil, err
	}
	mutation = mutation || len(changed) != 0
	if err := assignInitialSentinels(ctx, state, nil); err != nil {
		return nil, err
	}
	// Every newly admitted binding/name can expand header and echo rules
	// even on an already bound host. Only a proven identical alias keeps
	// the old admission constraints and its physical requests unchanged.
	for _, name := range names {
		if _, existing := current.secrets[name]; existing {
			continue
		}
		entry := state.secrets[name]
		if entry.bound != nil && !equivalentCurrentBound(current, entry) {
			authorities = append(authorities, entry.bound.binding.Hosts...)
		}
	}
	if err := validateLiveBoundSentinels(current, state); err != nil {
		return nil, err
	}
	if mutation || state.generation == "" {
		state.generation, err = registryNonce()
		if err != nil {
			return nil, err
		}
	}
	prepared, err := prepareBoundRegistry(ctx, state)
	if err != nil {
		return nil, err
	}
	prepared.secretOnly = slices.Equal(current.kinds, state.kinds)
	env := make(map[string]api.Secret, len(names))
	for _, name := range names {
		secret := state.secrets[name]
		if secret.bound != nil {
			env[name] = secret.bound.sentinel
		} else {
			env[name] = args.Values[name]
		}
	}
	slices.Sort(authorities)
	return &boundMemberCandidate{prepared: prepared, env: env, names: names, changed: changed, newAuthorities: slices.Compact(authorities), mutation: mutation}, nil
}

func equivalentCurrentBound(current *memberState, entry registeredSecret) bool {
	for _, old := range current.secrets {
		if sameAliasFormat(old, entry) && old.present == entry.present && old.present && old.value == entry.value && old.identity == entry.identity && old.bound.sentinel == entry.bound.sentinel {
			return true
		}
	}
	return false
}
