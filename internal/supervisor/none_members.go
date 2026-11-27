package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func (s *Supervisor) installNoneMemberV1(ctx context.Context, child *host.Manifest, approved map[string]api.Secret, intent *api.SecretDeliveryIntent, expected string) (secretEnvironment, error) {
	root := s.owner()
	if !root.unwalledV1() || !host.IsNoneSupervisor(child) {
		return secretEnvironment{}, secretIntentFailure()
	}
	if err := ctx.Err(); err != nil {
		return secretEnvironment{}, err
	}
	owned, _, err := host.PrepareSecretDelivery(intent, approved, false, recordedSecretNames(child.Secrets))
	if err != nil {
		return secretEnvironment{}, err
	}
	if owned != nil && len(owned.TrustedUses) != 0 {
		return secretEnvironment{}, secretIntentFailure()
	}
	root.polMu.Lock()
	defer root.polMu.Unlock()
	root.mu.Lock()
	current := api.CloneSessionSecretState(root.m.SecretState)
	root.mu.Unlock()
	if current != nil && expected != "" && current.Generation != expected {
		return secretEnvironment{}, secretIntentFailure()
	}
	if current != nil && len(approved) > 0 && expected == "" {
		return secretEnvironment{}, secretIntentFailure()
	}
	copy := host.CloneManifest(*child)
	if owned != nil {
		copy.SecretState = &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: *owned}
	}
	values := make(map[string]string, len(approved))
	for name, value := range approved {
		values[name] = string(value)
	}
	defer clear(values)
	environment, err := root.noneEnvironmentV1(&copy, values)
	if err != nil {
		return secretEnvironment{}, err
	}
	if copy.SecretState != nil {
		if current == nil {
			current = &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}, Generation: copy.SecretState.Generation, Primary: map[string]api.PublicSecretState{}}
		}
		for name, public := range copy.SecretState.Primary {
			if current.Intent.Modes == nil {
				current.Intent.Modes = make(map[string]api.SecretMode)
			}
			if current.Primary == nil {
				current.Primary = make(map[string]api.PublicSecretState)
			}
			if previous, exists := current.Intent.Modes[name]; exists && previous != api.SecretModeExposed {
				return secretEnvironment{}, secretIntentFailure()
			}
			current.Intent.Modes[name] = api.SecretModeExposed
			prior := current.Primary[name]
			public.Armed = public.Armed || prior.Armed
			current.Primary[name] = public
		}
		root.mu.Lock()
		root.m.SecretState = api.CloneSessionSecretState(current)
		root.dirty = true
		root.mu.Unlock()
	}
	return environment, nil
}

func (s *Supervisor) restoredNoneEnvironmentV1(ctx context.Context, child *Supervisor, legacy map[string]api.Secret) (secretEnvironment, error) {
	root := s.owner()
	values := make(map[string]string, len(legacy))
	for name, value := range legacy {
		values[name] = string(value)
	}
	defer clear(values)
	root.mu.Lock()
	plan := root.startupSecrets
	if plan != nil {
		process, exists := plan.Processes[child.id]
		if !exists {
			root.mu.Unlock()
			return secretEnvironment{}, secretIntentFailure()
		}
		for _, name := range process.Names {
			if process.Supplied[name] {
				values[name] = string(plan.Values[name])
			}
		}
	}
	root.mu.Unlock()
	child.mu.Lock()
	copy := host.CloneManifest(*child.m)
	child.mu.Unlock()
	environment, err := root.noneEnvironmentV1(&copy, values)
	if err != nil {
		return secretEnvironment{}, err
	}
	if copy.SecretState != nil {
		child.mu.Lock()
		child.m.SecretState = api.CloneSessionSecretState(copy.SecretState)
		child.mu.Unlock()
	}
	return environment, ctx.Err()
}

func (s *Supervisor) clearNoneMemberValuesV1() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, values := range s.memberValues {
		clear(values)
		delete(s.memberValues, id)
	}
}
