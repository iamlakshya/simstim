package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func secretCommitFailure() error {
	return &proto.Error{Code: "secret_state", Message: "the public secret change could not be confirmed",
		Hint: "Check the host's storage and matching helpers, then stop and resume the sandbox before retrying."}
}

func ownedSecretControl(s *Supervisor, owner *egressProc) bool {
	return s.m != nil && owner != nil && owner.ctl != nil && s.eg == owner && !owner.dead.Load() && !channelClosed(owner.ctl.done)
}

// commitSecretPublicState edits the CURRENT public envelope under the same
// durable writer. A pre-RPC snapshot cannot erase an intervening minted or
// retired record. Callers close owned networking on uncertain mutations.
func (s *Supervisor) commitSecretPublicState(ctx context.Context, owner *egressProc, generation *string, edit func(*api.SessionSecretState) (*api.SessionSecretState, error)) (*api.SessionSecretState, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var snapshot host.Manifest
	var result *api.SessionSecretState
	change := func(target *host.Manifest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		owned := ownedSecretControl(s, owner)
		s.mu.Unlock()
		if !owned || knownSessionSecretState(target.SecretState) != nil {
			return secretCommitFailure()
		}
		if generation != nil && (target.SecretState == nil || target.SecretState.Generation != *generation) {
			return secretCommitFailure()
		}
		next, err := edit(api.CloneSessionSecretState(target.SecretState))
		if err != nil {
			return err
		}
		initial, err := owner.ctl.publicInitial(*target)
		if err != nil {
			return err
		}
		if err := egress.ValidatePublicSessionState(initial, next, s.publicEgressCA(owner)); err != nil {
			return secretCommitFailure()
		}
		if err := checkSecretPublicResponse(next); err != nil {
			return err
		}
		target.SecretState = api.CloneSessionSecretState(next)
		return nil
	}
	publish := func(target *host.Manifest) {
		// The one writer holds s.mu, and no disk or RPC runs here.
		if !ownedSecretControl(s, owner) {
			return
		}
		if generation != nil && (s.m.SecretState == nil || s.m.SecretState.Generation != *generation) {
			return
		}
		s.m.SecretState = api.CloneSessionSecretState(target.SecretState)
		result = api.CloneSessionSecretState(target.SecretState)
	}
	if err := s.writeManifestChange(&snapshot, false, nil, nil, change, publish); err != nil {
		return nil, secretCommitFailure()
	}
	if result == nil {
		return nil, secretCommitFailure()
	}
	return result, nil
}
