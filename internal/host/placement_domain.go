package host

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// domain uses only this process's pinned canonical host identity and stable
// UID/HOME scope. Release metadata and remote inventory cannot select it.
func (p *placement) domain() cachefs.Domain {
	if p == nil {
		return cachefs.Domain{}
	}
	return cachefs.Domain{Scope: p.scope, HostID: p.hostID}
}

// bindStateDomain runs after the canonical host-id descriptor was validated.
// Only this constructor's exclusive new sentinel may acquire empty authority.
// Unknown older sentinels and another HOME's same-UID domain are preserved.
func (p *placement) bindStateDomain(ctx context.Context, state *os.Root, file *os.File, created bool) error {
	if err := p.domain().Validate(); err != nil {
		return err
	}
	unlock, err := waitPlacementFile(ctx, state, "place.lock", file, p.placeRef)
	if err != nil {
		return err
	}
	defer unlock()
	actual, found, err := cachefs.ReadDomainBinding(ctx, state, "place.lock", file)
	if err != nil {
		return err
	}
	if !found {
		if !created {
			return UserError("cache_domain", "the existing placement sentinel has no provable host-domain owner", "Preserve the recorded guard and sentinel and inspect their original owner before retrying.")
		}
		if _, err := cachefs.InitialiseDomainBinding(ctx, state, "place.lock", file, p.domain()); err != nil {
			return err
		}
	} else if actual != p.domain() {
		return UserError("cache_domain", "the configured state root belongs to another home or host identity", "Restore that domain's recorded roots or use disjoint state storage before retrying.")
	}
	p.placeRef, err = cachefs.ValidateFile(state, "place.lock", file, 0o600)
	return err
}

func (p *placement) checkStateDomain(ctx context.Context, state *os.Root, file *os.File) error {
	actual, found, err := cachefs.ReadDomainBinding(ctx, state, "place.lock", file)
	if err != nil {
		return err
	}
	if !found || actual != p.domain() || !actual.Known() {
		return UserError("cache_domain", "the placement sentinel no longer binds this exact host domain", "Keep the host stopped and inspect its original state, host identity and sentinel before retrying.")
	}
	return nil
}
