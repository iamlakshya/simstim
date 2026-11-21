package host

import "context"

// Only the existing connection's actual selection constructor uses this
// gate. It retains one failed original receipt, never a new ledger or a
// source reconstructed from a manifest, error, token or EOF.
func (s *runtimeService) lockRuntimeSelectionV1(ctx context.Context) (func(), error) {
	s.mu.Lock()
	if s.selectionGate == nil {
		s.selectionGate = make(chan struct{}, 1)
		s.selectionGate <- struct{}{}
	}
	gate := s.selectionGate
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-gate:
		if err := ctx.Err(); err != nil {
			gate <- struct{}{}
			return nil, err
		}
		return func() { gate <- struct{}{} }, nil
	}
}

func (s *runtimeService) retryRuntimeSelectionV1() error {
	s.mu.Lock()
	pending, key := s.selectionPending, s.selectionPendingKey
	s.mu.Unlock()
	if pending == nil {
		return nil
	}
	if err := pending.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.selectionPending == pending {
		if key != "" {
			if s.selections[key] != pending {
				s.mu.Unlock()
				return newRuntimeError("the failed original runtime selection changed during cleanup")
			}
			delete(s.selections, key)
		}
		s.selectionPending, s.selectionPendingKey = nil, ""
	}
	s.mu.Unlock()
	return nil
}

// All new native runtime constructors first drain the same retained failed
// receipt. A different manifest cannot bypass failed original cleanup.
func (s *runtimeService) retryRuntimeSelectionAdmissionV1(ctx context.Context) error {
	unlock, err := s.lockRuntimeSelectionV1(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return s.retryRuntimeSelectionV1()
}
