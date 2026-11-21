package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/supply"
)

func (s *runtimeService) enterRuntimeV1() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("runtime connection is closed")
	}
	s.active.Add(1)
	return s.active.Done, nil
}

// The original connection adopts an empty real owner before construction.
// Its active handler joins before connection cleanup can inspect that owner.
// The actual reserved slot remains held until every resource closes in order.
func (s *runtimeService) newRuntimeOwnerV1() (*runtimeOwnedPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reserve(); err != nil {
		return nil, err
	}
	owner := &runtimeOwnedPart{slot: true}
	if s.owned == nil {
		s.owned = map[*runtimeOwnedPart]struct{}{}
	}
	s.owned[owner] = struct{}{}
	return owner, nil
}

func (s *runtimeService) finishRuntimeOwnerV1(owner *runtimeOwnedPart) error {
	if owner == nil {
		return nil
	}
	if err := owner.close(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.owned, owner)
	for token, actual := range s.parts {
		if actual == owner {
			delete(s.parts, token)
		}
	}
	if owner.slot {
		owner.slot = false
		<-runtimeSlots
	}
	return nil
}

func (s *runtimeService) retryRuntimeOwnersV1() error {
	s.mu.Lock()
	owners := make([]*runtimeOwnedPart, 0, len(s.owned))
	for owner := range s.owned {
		owner.mu.Lock()
		closing := owner.closing
		owner.mu.Unlock()
		if closing {
			owners = append(owners, owner)
		}
	}
	s.mu.Unlock()
	for _, owner := range owners {
		if err := s.finishRuntimeOwnerV1(owner); err != nil {
			return err
		}
	}
	return nil
}

func (s *runtimeService) commitRuntimeOwnedV1(ctx context.Context, manifest supply.Manifest, defaults *[]supply.Item) (returned error) {
	owner, err := s.newRuntimeOwnerV1()
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, s.finishRuntimeOwnerV1(owner)) }()
	owner.publication, owner.store, err = openRuntimeProductionStore(ctx)
	if err != nil {
		return err
	}
	if err := commitRuntime(ctx, owner.store, manifest, defaults); err != nil {
		return err
	}
	return s.rememberRuntimeWithDefaults(ctx, manifest, defaults)
}
