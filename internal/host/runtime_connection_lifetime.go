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

// The caller holds this SAME original receipt's read lock. This bounded guard
// performs no file hashing and never reacquires placement control. Lock order
// is receipt.mu -> service.mu; no producer holds service.mu while taking mu.
func (receipt *selectedRuntimeReceiptV1) checkOriginLockedV1(ctx context.Context) error {
	if receipt == nil || ctx == nil || receipt.issuer == nil || receipt.key == "" || receipt.closed || receipt.publication == nil || receipt.store == nil || len(receipt.files) != len(receipt.manifest.Items) {
		return newRuntimeError("the original runtime writer lost its actual held producer")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	publication := receipt.publication
	if publication.owner == nil || publication.operation == nil || publication.operation.owner != publication.owner || publication.ctx.Err() != nil {
		return newRuntimeError("the original runtime publication ended or changed owner")
	}
	service := receipt.issuer
	service.mu.Lock()
	live := !service.closed && service.connection != nil && service.connection.Err() == nil && service.selections[receipt.key] == receipt
	service.mu.Unlock()
	if !live {
		return newRuntimeError("the runtime writer lost its SAME full-key receipt or authenticated connection")
	}
	return publication.owner.checkIdentity()
}
