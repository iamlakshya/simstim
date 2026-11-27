package supervisor

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

// persistMinted uses the one canonical durable writer. Only public minted
// metadata is edited, with no mu held through storage or control I/O.
func (c *egressCtl) persistMinted(ctx context.Context, event egress.MintedStateEvent) error {
	s := c.s
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var snapshot host.Manifest
	var binding api.SecretBind
	var saved api.PublicMintedSecretState
	published := false
	edit := func(target *host.Manifest) error {
		if !c.ownsMintedControl() {
			return mintedStateRefusal()
		}
		candidate, err := mintCandidate(ctx, target.SecretState, event)
		if err != nil {
			return err
		}
		initial, err := c.publicInitial(*target)
		if err != nil {
			return err
		}
		if err := egress.ValidatePublicSessionState(initial, candidate, s.publicEgressCA(c.owner)); err != nil {
			return mintedStateRefusal()
		}
		binding = candidate.Intent.Binds[event.Owner]
		for _, known := range candidate.Minted[event.Owner] {
			if known.Identity == event.State.Identity {
				saved = clonePublicMintedState(event.Owner, known)
				break
			}
		}
		target.SecretState = candidate
		return nil
	}
	publish := func(_ *host.Manifest) {
		// The shared writer holds mu here. Replace only this owned public
		// field, retaining any unrelated primary/global-generation changes.
		if !c.ownsMintedControlLocked() || !currentMintedBinding(s.m.SecretState, event.Owner, binding) {
			return
		}
		next := api.CloneSessionSecretState(s.m.SecretState)
		for _, known := range next.Minted[event.Owner] {
			if known.Identity == event.State.Identity {
				published = samePublicMintedState(known, event.State)
				return
			}
		}
		if next.Minted == nil {
			next.Minted = make(map[string][]api.PublicMintedSecretState)
		}
		next.Minted[event.Owner] = append(next.Minted[event.Owner], clonePublicMintedState(event.Owner, saved))
		s.m.SecretState = next
		published = true
	}
	if err := s.writeManifestChange(&snapshot, false, nil, nil, edit, publish); err != nil {
		return mintedWriteFailure(err)
	}
	if !published {
		return mintedStateRefusal()
	}
	return nil
}

func currentMintedBinding(state *api.SessionSecretState, owner string, binding api.SecretBind) bool {
	if state == nil || !state.Supported() || state.Intent.Modes[owner] != api.SecretModeBound || state.Primary[owner].Mode != api.SecretModeBound {
		return false
	}
	current, exists := state.Intent.Binds[owner]
	return exists && sameSecretBinding(current, binding)
}

func (c *egressCtl) currentMintedRecord(ctx context.Context, event egress.MintedStateEvent) bool {
	c.s.mu.Lock()
	if !c.ownsMintedControlLocked() || c.s.m.SecretState == nil || !c.s.m.SecretState.Supported() {
		c.s.mu.Unlock()
		return false
	}
	state := c.s.m.SecretState
	if state.Intent.Modes[event.Owner] != api.SecretModeBound || state.Primary[event.Owner].Mode != api.SecretModeBound {
		c.s.mu.Unlock()
		return false
	}
	binding := api.CloneSecretBinds(map[string]api.SecretBind{event.Owner: state.Intent.Binds[event.Owner]})[event.Owner]
	matched := false
	for _, known := range state.Minted[event.Owner] {
		if known.Identity == event.State.Identity {
			matched = samePublicMintedState(known, event.State)
			break
		}
	}
	c.s.mu.Unlock()
	return matched && egress.ValidatePublicMintedSecretState(ctx, event.Owner, event.State, binding) == nil
}

// runMintedEvents is one fixed worker, independent of audit I/O and polMu.
// An ACK can unblock a response which an in-flight policy RPC must join.
func (c *egressCtl) runMintedEvents(ctx context.Context) {
	defer c.s.wg.Done()
	defer close(c.minted.finished)
	defer c.minted.close()
	for {
		if channelClosed(c.done) {
			return
		}
		event, ok := c.minted.front()
		if !ok {
			select {
			case <-c.minted.wake:
			case <-c.done:
			}
			continue
		}
		durable := c.persistMinted(ctx, event) == nil
		if durable {
			durable = c.currentMintedRecord(ctx, event)
		}
		if c.ownsMintedControl() {
			// Every event's own labels are echoed, even if the current
			// global generation changed. The backend owns receipt epochs.
			ack := egress.SecretSavedArgs{Version: event.Version, Generation: event.Generation, Receipt: event.Receipt, Owner: event.Owner, Identity: event.State.Identity, Durable: durable}
			wait, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = c.request(wait, egress.CtlSecretSaved, ack, nil)
			cancel()
		}
		c.minted.complete()
	}
}
