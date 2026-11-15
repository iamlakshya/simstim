package client

import (
	"context"
	"errors"
)

// A logical New operation has ONE original settings source, including its
// pre-value runtime/image preparation and all terminal retries.
func (r *GrantRequest) borrowNewSettings(ctx context.Context) (*settingsOperation, error) {
	if r == nil {
		return nil, grantOperationError()
	}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.preparationSettings != nil {
		operation := r.preparationSettings
		r.mu.Unlock()
		return operation, operation.Revalidate(ctx)
	}
	lifetime := r.requestContext
	r.mu.Unlock()
	if lifetime == nil {
		return nil, grantOperationError()
	}
	operation, err := openSettingsOperation(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := operation.Settings(ctx)
	if err == nil {
		err = ValidateSettings(settings)
	}
	if err != nil {
		return nil, errors.Join(err, operation.Close())
	}
	r.mu.Lock()
	if r.preparationClosed || r.ending {
		r.mu.Unlock()
		return nil, errors.Join(grantOperationError(), operation.Close())
	}
	if r.preparationSettings == nil {
		if err := operation.retainRequestLifetime(ctx, lifetime); err != nil {
			r.mu.Unlock()
			return nil, errors.Join(err, operation.Close())
		}
		r.preparationSettings = operation
		r.mu.Unlock()
		return operation, nil
	}
	before := r.preparationSettings
	r.mu.Unlock()
	if err := operation.Close(); err != nil {
		return nil, err
	}
	return before, before.Revalidate(ctx)
}
