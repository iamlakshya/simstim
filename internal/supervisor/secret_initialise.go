package supervisor

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

func (s *Supervisor) modernSecretStartup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startupSecrets != nil || s.retainedSecrets != nil || s.m != nil && s.m.SecretState != nil {
		return true
	}
	for _, member := range s.knownMembers {
		if member.SecretState != nil {
			return true
		}
	}
	return false
}

func (s *Supervisor) clearStartupValues(primary map[string]string) {
	clear(primary)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startupSecrets != nil {
		clear(s.startupSecrets.Values)
		clear(s.startupSecrets.ProtectedTrustedSecretValues)
	}
}

func (s *Supervisor) publicEgressCA(owner *egressProc) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == nil || s.eg != owner {
		return ""
	}
	return owner.caCert
}

func (s *Supervisor) setPublicEgressCA(owner *egressProc, public string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ownedSecretControl(s, owner) {
		return secretCommitFailure()
	}
	owner.caCert = public
	return nil
}

func (s *Supervisor) initialiseEgress(ctx context.Context, owner *egressProc, initial egress.InitArgs, primary map[string]string) (secretEnvironment, error) {
	if err := requirePublicOnlyEgress(ctx, owner, initial.Policy); err != nil {
		return secretEnvironment{}, err
	}
	if !s.modernSecretStartup() {
		if err := checkMemberCapability(ctx, owner); err != nil {
			return secretEnvironment{}, err
		}
		var result egress.InitResult
		if err := owner.ctl.request(ctx, egress.CtlInit, initial, &result); err != nil {
			return secretEnvironment{}, wallError("the egress process rejected its initial policy", err)
		}
		if err := validatePublicSessionCA(result.CACert); err != nil {
			return secretEnvironment{}, err
		}
		if err := s.setPublicEgressCA(owner, result.CACert); err != nil {
			return secretEnvironment{}, err
		}
		environment := secretEnvironment{Env: make(map[string]string)}
		for _, name := range s.m.Secrets {
			if value, supplied := primary[name]; supplied {
				environment.Env[name] = value
			} else {
				environment.Missing = append(environment.Missing, name)
			}
		}
		return environment, nil
	}
	if err := requireBoundControl(ctx, owner); err != nil {
		return secretEnvironment{}, err
	}
	s.mu.Lock()
	snapshot := s.snapshot()
	fresh := s.startupSecrets
	retained := api.CloneSessionSecretState(s.retainedSecrets)
	s.mu.Unlock()
	if fresh == nil && len(primary) != 0 {
		return secretEnvironment{}, secretIntentFailure()
	}
	intentOnly, err := coldStartupIntent(snapshot, s.knownMembers, retained)
	if err != nil {
		return secretEnvironment{}, err
	}
	args, err := boundStartupArgs(ctx, initial, retained, fresh, intentOnly)
	if err != nil {
		return secretEnvironment{}, err
	}
	defer clear(args.Initial.Secrets)
	defer clear(args.ProtectedTrustedSecretValues)
	if err := requireTrustedJudgeControl(ctx, owner, args.TrustedUses); err != nil {
		return secretEnvironment{}, err
	}
	var raw json.RawMessage
	if err := owner.ctl.request(ctx, egress.CtlBoundInit, args, &raw); err != nil {
		return secretEnvironment{}, err
	}
	var result egress.BoundInitResult
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || egress.ValidateBoundInitialResult(ctx, args, result) != nil || validatePublicSessionCA(result.CACert) != nil {
		return secretEnvironment{}, secretCommitFailure()
	}
	if err := s.setPublicEgressCA(owner, result.CACert); err != nil {
		return secretEnvironment{}, err
	}
	environments, err := s.initialSecretEnvironments(ctx, snapshot, fresh, args, result)
	if err != nil {
		return secretEnvironment{}, err
	}
	state := &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: args.Modes, Binds: args.Binds, TrustedUses: api.CloneTrustedSecretUses(result.TrustedUses)},
		Generation: result.Generation, Primary: result.Secrets, Minted: result.Minted, Retired: result.Retired}
	if _, err := s.commitSecretPublicState(ctx, owner, nil, func(*api.SessionSecretState) (*api.SessionSecretState, error) { return state, nil }); err != nil {
		return secretEnvironment{}, err
	}
	s.mu.Lock()
	if s.startupSecrets == nil {
		s.startupSecrets = &startupSecretInput{}
	}
	s.startupSecrets.Environments = environments
	s.retainedSecrets = nil
	s.mu.Unlock()
	return environments[s.id], nil
}

func (s *Supervisor) initialSecretEnvironments(ctx context.Context, root host.Manifest, fresh *startupSecretInput, args egress.BoundInitArgs, result egress.BoundInitResult) (map[string]secretEnvironment, error) {
	environments := make(map[string]secretEnvironment)
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: args.Modes, Binds: args.Binds}
	for _, manifest := range append([]*host.Manifest{&root}, s.savedMembers...) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names := slices.Clone(manifest.Secrets)
		actual := make(map[string]string)
		if fresh != nil {
			process, exists := fresh.Processes[manifest.ID]
			if !exists {
				return nil, secretIntentFailure()
			}
			names = slices.Clone(process.Names)
			for name, supplied := range process.Supplied {
				if supplied {
					actual[name] = string(fresh.Values[name])
				}
			}
		}
		environment, err := secretEnvironmentFor(names, intent, result.Secrets, actual)
		clear(actual)
		if err != nil {
			return nil, err
		}
		environments[manifest.ID] = environment
	}
	return environments, nil
}
