package client

import (
	"context"
	"maps"

	"github.com/iamlakshya/simstim/internal/api"
)

// None keeps its existing delivery/legacy semantics. Its actual local fetch
// nevertheless borrows the SAME original settings and selected-name plan;
// a retry cannot reread configuration or rerun commands under another source.
func (p *newInputPreparation) fetchUnwalled(ctx context.Context, h Host, names []string, terminal bool) (map[string]api.Secret, error) {
	wall := requestWallIntent(ctx)
	p.mu.Lock()
	if p.closed || p.busy || !p.ready || p.payload || p.plan == nil || wall == nil || wall.level != "none" || p.selection == nil || !SameEntry(h, p.selection.host) ||
		!sameCanonicalSelectedNames(names, p.selection.names) {
		p.mu.Unlock()
		return nil, grantOperationError()
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.fetched {
		values := maps.Clone(p.values)
		p.mu.Unlock()
		return values, nil
	}
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if err := bindRequestLocalSource(ctx, h); err != nil {
		return nil, err
	}
	values, err := FetchSecrets(ctx, settings, names, terminal)
	if err != nil {
		return nil, err
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return nil, err
	}
	if err := bindRequestLocalSource(ctx, h); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.values, p.fetched = maps.Clone(values), true
	p.mu.Unlock()
	return values, nil
}
