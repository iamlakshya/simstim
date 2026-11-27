package client

import (
	"context"
	"maps"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// Values remain inside the actual fresh plan until its one spawn writer. No
// public cohort, parent allowance or raw name list can select a command here.
func (p *freshChildPlan) fetch(ctx context.Context, terminal bool) error {
	p.mu.Lock()
	if p.closed || p.aborting || p.retired || p.attempted || p.published || p.busy || p.creation == nil {
		p.mu.Unlock()
		return joinIntentError()
	}
	p.busy = true
	already := p.fetched
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	if err := p.authorise(ctx); err != nil {
		return err
	}
	if err := p.revalidateCohort(ctx); err != nil {
		return err
	}
	if already {
		return nil
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return err
	}
	values := make(map[string]api.Secret, len(p.selection.names))
	for _, name := range p.selection.names {
		// Revoke a removed Source or ended request before EACH actual value
		// command, including commands after a previous slow password prompt.
		if err := p.authorise(ctx); err != nil {
			return err
		}
		value, err := runSecret(ctx, name, view.Commands[name], terminal)
		if err != nil {
			return err
		}
		values[name] = value
	}
	if err := p.authorise(ctx); err != nil {
		return err
	}
	if err := p.revalidateCohort(ctx); err != nil {
		return err
	}
	if err := egress.ValidateFreshValuesAgainstPublicState(ctx, p.cohort.SecretState, values); err != nil {
		return err
	}
	p.mu.Lock()
	p.values, p.fetched = maps.Clone(values), true
	p.mu.Unlock()
	return nil
}
