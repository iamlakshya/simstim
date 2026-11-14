package runtimecache

import (
	"context"
	"fmt"
)

// PublicationGate runs publish while a host's final placement admission holds.
// Receive, network and descriptor verification happen before this callback.
// Image leases, when applicable, precede placement and this store transaction.
type PublicationGate func(context.Context, func() error) error

// OpenWithPublicationGate opens the canonical writable store and binds a final
// publication admission callback. The caller first reserves its active host
// publisher, and admits constructor filesystem changes under placement too.
func OpenWithPublicationGate(dir string, gate PublicationGate) (*Store, error) {
	if gate == nil {
		return nil, fmt.Errorf("runtime publication requires an explicit gate")
	}
	store, err := openStore(dir, true)
	if err != nil {
		return nil, err
	}
	store.gate = gate
	return store, nil
}

func (s *Store) withPublication(ctx context.Context, publish func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.gate != nil {
		return s.gate(ctx, publish)
	}
	return publish()
}
