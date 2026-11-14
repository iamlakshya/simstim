package supervisor

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// restoredSecretEnvironment uses the already committed complete init bundle.
// Replaying old supplied values through member.add would undo a later Arm.
func (s *Supervisor) restoredSecretEnvironment(ctx context.Context, child *Supervisor, legacy map[string]api.Secret) (secretEnvironment, error) {
	root := s.owner()
	root.mu.Lock()
	plan := root.startupSecrets
	modern := root.m.SecretState != nil
	var environment secretEnvironment
	var exists bool
	if plan != nil {
		environment, exists = plan.Environments[child.id]
	}
	current := api.CloneSessionSecretState(root.m.SecretState)
	owner := root.eg
	ready := ownedSecretControl(root, owner)
	root.mu.Unlock()
	if !modern {
		return root.installMemberPolicy(ctx, child.m, legacy, nil, "")
	}
	if !exists || !ready || current == nil || !current.Supported() {
		return secretEnvironment{}, secretCommitFailure()
	}
	for name, mode := range environment.Modes {
		if mode == api.SecretModeBound && (current.Primary[name].Mode != mode || string(current.Primary[name].Sentinel) != environment.Env[name]) {
			return secretEnvironment{}, secretCommitFailure()
		}
	}
	if err := root.requireBoundGuest(ctx); err != nil {
		return secretEnvironment{}, err
	}
	environment.Env, environment.Modes = maps.Clone(environment.Env), maps.Clone(environment.Modes)
	environment.Missing = slices.Clone(environment.Missing)
	return environment, nil
}

// installPublicSecretView records a process subset without becoming another
// engine or CAS source. Explicit info always projects CURRENT root state.
// Root's complete envelope was durably saved before this process can start.
func (s *Supervisor) installPublicSecretView(intent *api.SecretDeliveryIntent) error {
	root := s.owner()
	if root == s {
		return nil
	}
	root.mu.Lock()
	current := api.CloneSessionSecretState(root.m.SecretState)
	root.mu.Unlock()
	if current == nil {
		return nil // genuine legacy path has no public binding metadata
	}
	if !current.Supported() {
		return secretIntentFailure()
	}
	s.mu.Lock()
	names := slices.Clone(s.m.Secrets)
	s.mu.Unlock()
	selected := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind)}
	if intent != nil {
		selected = api.CloneSecretDeliveryIntent(*intent)
	} else {
		for _, name := range names {
			mode, exists := current.Intent.Modes[name]
			if !exists {
				return secretIntentFailure()
			}
			selected.Modes[name] = mode
			if mode == api.SecretModeBound {
				selected.Binds[name] = current.Intent.Binds[name]
			}
		}
	}
	state := &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: selected, Generation: current.Generation,
		Primary: make(map[string]api.PublicSecretState), Minted: make(map[string][]api.PublicMintedSecretState), Retired: make(map[string][]api.RetiredPrimarySecretState)}
	for name, mode := range selected.Modes {
		public, present := current.Primary[name]
		if !present && mode == api.SecretModeExposed {
			public = api.PublicSecretState{Mode: mode} // declared missing exposure
		} else if !present || public.Mode != mode {
			return secretIntentFailure()
		}
		state.Primary[name] = public
		if entries := current.Minted[name]; len(entries) > 0 {
			state.Minted[name] = entries
		}
		if entries := current.Retired[name]; len(entries) > 0 {
			state.Retired[name] = entries
		}
	}
	state = api.CloneSessionSecretState(state)
	if err := egress.ValidateSavedPublicSessionState(state); err != nil {
		return err
	}
	s.mu.Lock()
	s.m.SecretState = state
	s.dirty = true
	s.mu.Unlock()
	return nil
}
