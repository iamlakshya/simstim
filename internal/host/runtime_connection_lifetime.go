package host

import "context"

// The accepted bridge calls this once with its ACTUAL original context.
// No job/request, copied manifest or expired receipt can replace this lifetime.
func (s *runtimeService) bindConnectionV1(ctx context.Context) error {
	if ctx == nil {
		return newRuntimeError("the runtime service has no original connection lifetime")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.connection != nil {
		return newRuntimeError("the runtime service cannot replace its original connection lifetime")
	}
	s.connection, s.cancelConnection = context.WithCancel(ctx)
	return nil
}

// Construction is request-cancellable and service-cancellable. Only the
// publication's parent outlives a successfully completed transfer request.
func (s *runtimeService) selectionContextV1(request context.Context) (context.Context, context.Context, func(), error) {
	if request == nil {
		return nil, nil, nil, newRuntimeError("the runtime request has no actual owner context")
	}
	if err := request.Err(); err != nil {
		return nil, nil, nil, err
	}
	s.mu.Lock()
	connection, closed := s.connection, s.closed
	s.mu.Unlock()
	if closed || connection == nil {
		return nil, nil, nil, newRuntimeError("the runtime producer has no accepted connection lifetime")
	}
	if err := connection.Err(); err != nil {
		return nil, nil, nil, err
	}
	owned, cancel := context.WithCancel(request)
	joined := make(chan struct{})
	stop := context.AfterFunc(connection, func() { defer close(joined); cancel() })
	release := func() {
		if !stop() {
			<-joined
		}
		cancel()
	}
	return connection, owned, release, nil
}

func (receipt *selectedRuntimeReceiptV1) producerEndedV1() bool {
	if receipt == nil {
		return true
	}
	receipt.mu.RLock()
	defer receipt.mu.RUnlock()
	return receipt.closed || receipt.publication == nil || receipt.publication.ctx.Err() != nil
}

// Only a fresh actual status/delivery constructor calls this under the
// existing selection gate. Lookup/use never retires, reopens or renews it.
func (s *runtimeService) retireEndedSelectionV1(key string, receipt *selectedRuntimeReceiptV1) error {
	if receipt == nil || receipt.issuer != s {
		return newRuntimeError("the original runtime receipt belongs to another connection issuer")
	}
	if err := receipt.Close(); err != nil {
		s.mu.Lock()
		s.selectionPending, s.selectionPendingKey = receipt, key
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selections[key] != receipt {
		return newRuntimeError("the original runtime selection changed during native retirement")
	}
	delete(s.selections, key)
	return nil
}
