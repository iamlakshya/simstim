package client

import (
	"context"
	"maps"

	"github.com/iamlakshya/simstim/internal/api"
)

// Successful guest reads stay in the same original plan even when a later
// guest, trusted-role or named-key phase fails. Only all completed phases set
// p.fetched. No retry can rerun an earlier successful credential command.
func fetchNewGuestValuesV1(ctx context.Context, p *newSecretPlan, settings Settings, names []string, terminal bool) (map[string]api.Secret, error) {
	p.mu.Lock()
	if p.guestFetched {
		values := maps.Clone(p.values)
		p.mu.Unlock()
		return values, nil
	}
	p.mu.Unlock()
	for _, name := range names {
		if err := p.operation.Revalidate(ctx); err != nil {
			return nil, err
		}
		if err := bindRequestLocalSource(ctx, p.conn.Host); err != nil {
			return nil, err
		}
		if err := checkTrustedJudgeNewBeforeValuesV1(ctx, p); err != nil {
			return nil, err
		}
		p.mu.Lock()
		_, fetched := p.values[name]
		p.mu.Unlock()
		if fetched {
			continue
		}
		one, err := FetchSecrets(ctx, settings, []string{name}, terminal)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.values == nil {
			p.values = make(map[string]api.Secret, len(names))
		}
		p.values[name] = one[name]
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.guestFetched = true
	values := maps.Clone(p.values)
	p.mu.Unlock()
	return values, nil
}
