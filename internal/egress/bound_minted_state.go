package egress

import (
	"context"
	"maps"
	"slices"
)

func publicMintedState(ctx context.Context, name string, mint registeredMint) (PublicMintedSecretState, error) {
	shape, err := publicSecretShape(ctx, name, mint.shape, mint.rule)
	if err != nil {
		return PublicMintedSecretState{}, err
	}
	return PublicMintedSecretState{Sentinel: mint.sentinel, Shape: shape, Identity: mint.identity, Authority: mint.authority, Rule: mint.rule}, nil
}

func (state *memberState) publicMinted(ctx context.Context) (map[string][]PublicMintedSecretState, error) {
	output := make(map[string][]PublicMintedSecretState)
	for _, name := range slices.Sorted(maps.Keys(state.secrets)) {
		for _, mint := range state.secrets[name].minted {
			// Pending reservations have never been offered for durable save
			// by an initial result. Their own exact receipt owns publication.
			if !mint.ready && !mint.retired {
				continue
			}
			public, err := publicMintedState(ctx, name, mint)
			if err != nil {
				return nil, err
			}
			output[name] = append(output[name], public)
		}
	}
	return output, nil
}

func restoreMintedState(ctx context.Context, state *memberState, saved map[string][]PublicMintedSecretState) error {
	for _, name := range slices.Sorted(maps.Keys(saved)) {
		owner, present := state.secrets[name]
		if !present || owner.bound == nil {
			return registryIntentError()
		}
		for _, public := range saved[name] {
			shape, err := validatedMintedShape(ctx, name, public, owner.bound.binding)
			if err != nil {
				return err
			}
			identity, err := registryNonce()
			if err != nil {
				return err
			}
			owner.minted = append(owner.minted, registeredMint{sentinel: public.Sentinel, shape: shape, rule: public.Rule, authority: public.Authority, identity: identity, retired: true})
		}
		state.secrets[name] = owner
	}
	return nil
}
