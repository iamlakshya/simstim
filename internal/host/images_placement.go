package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The connection owns one real publication reservation before any image root
// or copied part exists. It retains that reservation through every transfer's
// exact cleanup; only the Store's short final gate publishes stable names.
func (s *imageService) open(ctx context.Context) (*image.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("image connection has ended")
	}
	if s.store != nil {
		if s.publication.ctx.Err() != nil {
			return nil, context.Cause(s.publication.ctx)
		}
		return s.store, nil
	}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return nil, err
	}
	store, err := image.OpenHost(publication.ctx, paths.ImagesDir(), publication.owner.domain(), publication.gate)
	if err != nil {
		return nil, errors.Join(err, publication.close())
	}
	s.publication, s.store = publication, store
	s.transfers = map[string]*image.Transfer{}
	go func() { <-publication.ctx.Done(); _ = s.close() }()
	return store, nil
}

func openImageInventory(ctx context.Context) (*image.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner, err := placementForCall()
	if err != nil {
		return nil, err
	}
	return image.OpenHostExisting(ctx, paths.ImagesDir(), owner.domain())
}

// operationContext preserves request cancellation and the reservation's drain
// cancellation. No native store lock or placement gate spans RPC/conversion.
func (s *imageService) operationContext(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	publication := s.publication
	closed := s.closed
	s.mu.Unlock()
	if closed || publication == nil {
		return nil, nil, fmt.Errorf("image publication is no longer active")
	}
	if publication.ctx.Err() != nil {
		return nil, nil, context.Cause(publication.ctx)
	}
	owned, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(publication.ctx, func() { cancel(context.Cause(publication.ctx)) })
	return owned, func() { stop(); cancel(context.Canceled) }, nil
}

func (s *imageService) close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		for _, job := range s.jobs {
			job.cancel()
		}
		s.mu.Unlock()
		s.active.Wait()
		s.mu.Lock()
		for _, transfer := range s.transfers {
			s.closeErr = errors.Join(s.closeErr, transfer.Close())
		}
		if s.store != nil {
			s.closeErr = errors.Join(s.closeErr, s.store.Close())
		}
		publication := s.publication
		s.mu.Unlock()
		if publication != nil {
			s.closeErr = errors.Join(s.closeErr, publication.close())
		}
	})
	return s.closeErr
}
