package supervisor

import (
	"context"
	"errors"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func mintedStateRefusal() error {
	return &proto.Error{Code: "secret_state", Message: "the minted public state was not confirmed",
		Hint: "Check the host's storage and matching helpers, then retry with approved bound secrets."}
}

func sameSecretBinding(a, b api.SecretBind) bool {
	return a.Header == b.Header && a.Format == b.Format && slices.Equal(a.Hosts, b.Hosts)
}

// mintCandidate changes only public minted metadata. Event generation is a
// receipt correlation label, never authority to replace the current epoch.
func mintCandidate(ctx context.Context, current *api.SessionSecretState, event egress.MintedStateEvent) (*api.SessionSecretState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if event.Version != egress.MemberRegistryV2 || event.Generation == "" || event.Receipt == "" || current == nil || !current.Supported() || current.Intent.Modes[event.Owner] != api.SecretModeBound {
		return nil, mintedStateRefusal()
	}
	primary, exists := current.Primary[event.Owner]
	binding, bound := current.Intent.Binds[event.Owner]
	if !exists || primary.Mode != api.SecretModeBound || !bound {
		return nil, mintedStateRefusal()
	}
	if err := egress.ValidatePublicMintedSecretState(ctx, event.Owner, event.State, binding); err != nil {
		return nil, mintedStateRefusal()
	}
	for _, known := range current.Minted[event.Owner] {
		if known.Identity == event.State.Identity {
			if !samePublicMintedState(known, event.State) {
				return nil, mintedStateRefusal()
			}
			return api.CloneSessionSecretState(current), nil
		}
	}
	candidate := api.CloneSessionSecretState(current)
	if candidate.Minted == nil {
		candidate.Minted = make(map[string][]api.PublicMintedSecretState)
	}
	candidate.Minted[event.Owner] = append(candidate.Minted[event.Owner], clonePublicMintedState(event.Owner, event.State))
	return candidate, nil
}

func (c *egressCtl) ownsMintedControlLocked() bool {
	return c.s.m != nil && c.owner != nil && c.s.eg == c.owner && !c.owner.dead.Load() && !channelClosed(c.done)
}

func (c *egressCtl) ownsMintedControl() bool {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return c.ownsMintedControlLocked()
}

func mintedWriteFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return mintedStateRefusal()
}
