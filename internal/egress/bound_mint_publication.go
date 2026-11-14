package egress

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
)

// A receipt is lifetime bookkeeping inside one canonical minted record. It
// carries no extra real value or authorisation registry. Active HTTP request
// ownership bounds waiters. No detached sender or waiter goroutine is added.
type mintPublication struct {
	receipt, generation, epoch, parent, owner, identity, authority string
	state                                                          *mintPublicationState
}

type mintPublicationState struct {
	started           atomic.Bool
	sent, done        chan struct{}
	mu                sync.Mutex
	finished, durable bool
	waiters           map[*mintWaiter]struct{}
}

type mintWaiter struct {
	ctx context.Context
	use secretRequestUse
}

type mintReservation struct {
	name, epoch, parent, identity, authority string
	sentinel                                 api.Secret
	publication                              *mintPublication
	waiter                                   *mintWaiter
	event                                    MintedStateEvent
}

func (mintPublication) Format(f fmt.State, _ rune)       { _, _ = io.WriteString(f, api.Redacted) }
func (*mintPublicationState) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (mintWaiter) Format(f fmt.State, _ rune)            { _, _ = io.WriteString(f, api.Redacted) }
func (mintReservation) Format(f fmt.State, _ rune)       { _, _ = io.WriteString(f, api.Redacted) }

func newMintPublication(state *memberState, owner string, mint registeredMint) (*mintPublication, error) {
	receipt, err := registryNonce()
	if err != nil {
		return nil, err
	}
	secret := state.secrets[owner]
	return &mintPublication{receipt: receipt, generation: state.generation, epoch: secret.valueEpoch,
		parent: secret.identity, owner: owner, identity: mint.identity, authority: mint.authority,
		state: &mintPublicationState{sent: make(chan struct{}), done: make(chan struct{}), waiters: make(map[*mintWaiter]struct{})}}, nil
}

func (p *mintPublication) addWaiter(ctx context.Context, use secretRequestUse) *mintWaiter {
	waiter := &mintWaiter{ctx: ctx, use: secretRequestUse{Names: slices.Clone(use.Names), Identities: slices.Clone(use.Identities)}}
	p.state.mu.Lock()
	if p.state.finished {
		p.state.mu.Unlock()
		return nil
	}
	p.state.waiters[waiter] = struct{}{}
	p.state.mu.Unlock()
	return waiter
}

func (p *mintPublication) removeWaiter(waiter *mintWaiter) {
	p.state.mu.Lock()
	delete(p.state.waiters, waiter)
	if len(p.state.waiters) == 0 && !p.state.finished {
		p.state.finished = true
		close(p.state.done)
	}
	p.state.mu.Unlock()
}

func (p *mintPublication) finish(durable bool) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if !p.state.finished {
		p.state.finished, p.state.durable = true, durable
		close(p.state.done)
	}
}

func (p *mintPublication) result() (bool, bool) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	return p.state.finished, p.state.durable
}

func (p *mintPublication) liveWaiter(state *memberState) bool {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	for waiter := range p.state.waiters {
		if waiter.ctx.Err() == nil && validMintUse(state, p.owner, p.authority, p.parent, p.epoch, waiter.use) {
			return true
		}
	}
	return false
}

func validMintUse(state *memberState, name, authority, parent, epoch string, use secretRequestUse) bool {
	owner, present := state.secrets[name]
	if !present || owner.bound == nil || !owner.present || owner.identity != parent || owner.valueEpoch != epoch || !slices.Contains(owner.bound.binding.Hosts, authority) {
		return false
	}
	if len(use.Names) == 0 && len(use.Identities) == 0 {
		return true
	}
	parents, err := responseUsedIdentities(state, authority, use)
	return err == nil && slices.Contains(parents, parent)
}

func secretSaveFailure() error {
	return memberError("secret_save", "server-issued credential was not durably published", "Re-mint the credential or start a fresh session after fixing public-state persistence.")
}

func (s *Server) finishMintReservation(ctx context.Context, reservation *mintReservation, use secretRequestUse) (api.Secret, error) {
	if reservation.publication != nil {
		p := reservation.publication
		defer p.removeWaiter(reservation.waiter)
		if p.state.started.CompareAndSwap(false, true) {
			if err := s.emitMintedState(ctx, reservation.event); err != nil {
				p.finish(false)
			}
			close(p.state.sent)
		} else {
			select {
			case <-p.state.sent:
			case <-ctx.Done():
				return "", secretSaveFailure()
			}
		}
		select {
		case <-p.state.done:
		case <-ctx.Done():
			return "", secretSaveFailure()
		}
		if _, durable := p.result(); !durable {
			return "", secretSaveFailure()
		}
	}
	if err := s.policyMu.LockContext(ctx); err != nil {
		return "", secretSaveFailure()
	}
	defer s.policyMu.Unlock()
	state := s.currentMembers()
	if state == nil || !validMintUse(state, reservation.name, reservation.authority, reservation.parent, reservation.epoch, use) {
		return "", secretSaveFailure()
	}
	owner := state.secrets[reservation.name]
	if reservation.identity == owner.identity && reservation.sentinel == owner.bound.sentinel {
		return reservation.sentinel, nil
	}
	for _, mint := range owner.minted {
		if mint.identity == reservation.identity && mint.authority == reservation.authority && mint.sentinel == reservation.sentinel && mint.ready && !mint.retired && (reservation.publication == nil || mint.publication == reservation.publication) {
			return reservation.sentinel, nil
		}
	}
	return "", secretSaveFailure()
}
