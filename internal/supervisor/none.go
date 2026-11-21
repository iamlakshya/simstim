package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

type noneRuntime struct{ wall.Sandbox }

func init() {
	Features = append(Features, proto.FeatureNoneSupervisorV1, proto.FeatureNoneStopCompletionV1)
}

func prepareNoneV1(ctx context.Context, m *host.Manifest) (sandbox, error) {
	if !host.NoneSupervisorShapeV1(m) {
		return nil, execRefusal("the unwalled runtime has a conflicting wall or policy source")
	}
	runtime, err := (none.Driver{}).Prepare(ctx, wall.Spec{ID: m.ID, SessionDir: paths.SessionDir(m.ID), MemoryMax: m.Limits.MemoryMax})
	if err != nil {
		return nil, err
	}
	return noneRuntime{Sandbox: runtime}, nil
}

func (r noneRuntime) Wait() error { return r.Sandbox.Wait().Err }
func (r noneRuntime) CleanExit() bool {
	owner, ok := r.Sandbox.(interface{ CleanExit() bool })
	return ok && owner.CleanExit()
}

func (r noneRuntime) cleanupConfirmedV1() bool {
	owner, ok := r.Sandbox.(interface{ CleanupCompleteV1() bool })
	return ok && owner.CleanupCompleteV1()
}

func (r noneRuntime) preparedCleanupConfirmedV1() bool {
	owner, ok := r.Sandbox.(interface{ PreparedCleanupCompleteV1() bool })
	return ok && owner.PreparedCleanupCompleteV1()
}
func (r noneRuntime) closePreparedResourcesV1() error {
	if physical, ok := r.Sandbox.(interface{ PhysicalExitV1() bool }); ok && physical.PhysicalExitV1() {
		return nil
	}
	prepared, ok := r.Sandbox.(interface{ ReleasePreparedV1() error })
	if !ok {
		return execRefusal("the unwalled runtime has no original prepared-resource owner")
	}
	return prepared.ReleasePreparedV1()
}

func (s *Supervisor) unwalledV1() bool { return host.IsNoneSupervisor(s.owner().m) }

// noneEnvironmentV1 uses only the original accepted process's approved
// exposure names and actual values. No egress engine, stand-in or arm is made.
func (s *Supervisor) noneEnvironmentV1(m *host.Manifest, values map[string]string) (secretEnvironment, error) {
	if !host.IsNoneSupervisor(m) {
		return secretEnvironment{}, secretIntentFailure()
	}
	approved := make(map[string]api.Secret, len(values))
	for name, value := range values {
		if !slices.Contains(m.Secrets, name) {
			return secretEnvironment{}, secretIntentFailure()
		}
		approved[name] = api.Secret(value)
	}
	defer clear(approved)
	var intent *api.SecretDeliveryIntent
	if m.SecretState != nil {
		copy := api.CloneSecretDeliveryIntent(m.SecretState.Intent)
		intent = &copy
	} else {
		intent = &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind)}
		for _, name := range m.Secrets {
			intent.Modes[name] = api.SecretModeExposed
		}
	}
	owned, _, err := host.PrepareSecretDelivery(intent, approved, false, recordedSecretNames(m.Secrets))
	if err != nil {
		return secretEnvironment{}, err
	}
	if owned != nil && len(owned.TrustedUses) != 0 {
		return secretEnvironment{}, secretIntentFailure()
	}
	result := secretEnvironment{Env: make(map[string]string, len(m.Secrets))}
	for _, name := range m.Secrets {
		if value, present := values[name]; present {
			result.Env[name] = value
		} else {
			result.Missing = append(result.Missing, name)
		}
	}
	if owned != nil {
		root := s.owner()
		root.mu.Lock()
		generation := ""
		if root.m.SecretState != nil {
			generation = root.m.SecretState.Generation
		}
		root.mu.Unlock()
		if generation == "" {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return secretEnvironment{}, err
			}
			generation = hex.EncodeToString(random[:])
		}
		state := &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: *owned, Generation: generation, Primary: make(map[string]api.PublicSecretState, len(owned.Modes))}
		for name, mode := range owned.Modes {
			if mode != api.SecretModeExposed {
				return secretEnvironment{}, secretIntentFailure()
			}
			_, present := values[name]
			state.Primary[name] = api.PublicSecretState{Mode: api.SecretModeExposed, Armed: present}
		}
		m.SecretState = state
	}
	return result, nil
}

func (s *Supervisor) startNoneEnvironmentV1(ctx context.Context, values map[string]string) (secretEnvironment, error) {
	if err := ctx.Err(); err != nil {
		return secretEnvironment{}, err
	}
	s.mu.Lock()
	m := host.CloneManifest(*s.m)
	s.mu.Unlock()
	result, err := s.noneEnvironmentV1(&m, values)
	if err != nil {
		return secretEnvironment{}, err
	}
	s.mu.Lock()
	s.m.SecretState = api.CloneSessionSecretState(m.SecretState)
	s.dirty = true
	s.memberReady = true
	s.mu.Unlock()
	return result, nil
}
