package client

import (
	"context"
	"errors"
)

func (m *agentModeResources) newNativeLog(ctx context.Context) (*agentNativeLog, error) {
	m.mu.Lock()
	if err := m.checkLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if m.nativeLog != nil {
		m.mu.Unlock()
		return nil, agentModeResourceError()
	}
	log, err := makeAgentNativeLogAtDirectory(ctx, m.directory, false)
	if log != nil {
		log.mode, m.nativeLog = m, log
	}
	m.mu.Unlock()
	if err != nil {
		if log != nil {
			err = errors.Join(err, log.Close())
		}
		return nil, err
	}
	return log, nil
}

func (m *agentModeResources) newCollectorSocket(ctx context.Context) (*agentCollectorSocket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return nil, err
	}
	return newAgentCollectorSocketAtDirectory(m.directory)
}
