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
	s.openMu.Lock()
	defer s.openMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("image connection has ended")
	}
	if s.closeErr != nil {
		publication, store := s.publication, s.store
		s.mu.Unlock()
		if err := s.closeImageOpenV1(publication, store); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, fmt.Errorf("image connection has ended")
		}
	}
	if s.store != nil {
		store, publication := s.store, s.publication
		s.mu.Unlock()
		if publication.ctx.Err() != nil {
			return nil, context.Cause(publication.ctx)
		}
		return store, nil
	}
	if s.connection == nil {
		s.connection = ctx
	}
	connection := s.connection
	pending := s.publication
	s.mu.Unlock()
	if pending != nil {
		if err := s.closeImageOpenV1(pending, nil); err != nil {
			return nil, err
		}
	}
	// Abort must be able to acquire mu and cancel this request while the
	// original publication reservation or store is being acquired.
	publication, err := beginImagePublicationV1(ctx, connection)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.publication = publication // Adopt before any subsequent failure branch.
	s.mu.Unlock()
	if err := publication.prepareStorageFootprintsV1(publication.ctx, "images"); err != nil {
		return nil, errors.Join(err, s.closeImageOpenV1(publication, nil))
	}
	store, err := image.OpenHost(publication.ctx, paths.ImagesDir(), publication.owner.domain(), publication.gate)
	s.mu.Lock()
	s.store = store
	closed := s.closed
	s.mu.Unlock()
	if err != nil {
		return nil, errors.Join(err, s.closeImageOpenV1(publication, store))
	}
	if closed {
		return nil, errors.Join(fmt.Errorf("image connection has ended"), s.closeImageOpenV1(publication, store))
	}
	s.mu.Lock()
	if s.transfers == nil {
		s.transfers = map[string]*image.Transfer{}
	}
	s.mu.Unlock()
	go func() { <-publication.ctx.Done(); _ = s.close() }()
	return store, nil
}

// Called with openMu held. The connection keeps failed partial-open objects;
// no new publisher can replace them until this same ordered cleanup succeeds.
func (s *imageService) closeImageOpenV1(publication *runtimePublication, store *image.Store) error {
	if store != nil {
		if err := store.Close(); err != nil {
			s.mu.Lock()
			s.closeErr = err
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		if s.store == store {
			s.store = nil
		}
		s.mu.Unlock()
	}
	if err := publication.close(); err != nil {
		s.mu.Lock()
		s.closeErr = err
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	if s.publication == publication {
		s.publication = nil
	}
	s.closeErr = nil
	s.mu.Unlock()
	return nil
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
	return context.WithValue(owned, publicationSourceContextV1{}, publication.operation), func() { stop(); cancel(context.Canceled) }, nil
}

func (s *imageService) close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	s.closed = true
	for _, job := range s.jobs {
		job.cancel()
	}
	s.mu.Unlock()
	s.active.Wait()
	s.openMu.Lock()
	defer s.openMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	var result error
	for token, transfer := range s.transfers {
		if err := transfer.Close(); err != nil {
			result = errors.Join(result, err)
		} else {
			delete(s.transfers, token)
		}
	}
	if result != nil {
		// Preserve the SAME store, creator capabilities and publication beneath
		// unfinished cleanup. A second Close retries this original source.
		s.closeErr = result
		return result
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			s.closeErr = err
			return err
		}
		s.store = nil
	}
	if s.publication != nil {
		if err := s.publication.close(); err != nil {
			s.closeErr = err
			return err
		}
		s.publication = nil
	}
	s.closeErr = nil
	return nil
}
