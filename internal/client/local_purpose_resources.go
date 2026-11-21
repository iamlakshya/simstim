package client

import (
	"context"
	"errors"
)

func (r *GrantRequest) borrowMetadataMode(ctx context.Context) (*agentModeResources, error) {
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.metadataMode != nil {
		mode := r.metadataMode
		r.mu.Unlock()
		return mode, mode.checkAgentSettingsMode(ctx)
	}
	r.mu.Unlock()
	mode, err := newAgentModeResources(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return nil, errors.Join(err, mode.closeInactive())
	}
	if r.metadataMode == nil {
		r.metadataMode = mode
		r.mu.Unlock()
		return mode, nil
	}
	before := r.metadataMode
	r.mu.Unlock()
	if err := mode.closeInactive(); err != nil {
		return nil, err
	}
	return before, before.checkAgentSettingsMode(ctx)
}

// This directory-only mode has no collector/log and the caller has already
// joined every query. It adds no grace; a live index is retained for retry.
func (m *agentModeResources) closeInactive() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.cancel()
	if m.active != nil || m.nativeLog != nil {
		return agentModeResourceError()
	}
	m.cleanupLocked()
	return m.closeErr
}

func (r *GrantRequest) closeLocalPurposeResources() error {
	r.mu.Lock()
	forge, execution, mode, accounts := r.forge, r.rootExecution, r.metadataMode, r.rootAccounts
	r.mu.Unlock()
	if err := accounts.close(); err != nil {
		return err
	}
	if forge != nil {
		if err := forge.close(); err != nil {
			return err
		}
	}
	if execution != nil {
		if err := execution.close(); err != nil {
			return err
		}
	}
	return mode.closeInactive()
}
