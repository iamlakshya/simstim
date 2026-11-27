package runtimecache

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// OpenHost admits a host store only with its pinned scope, checked HostID and
// final publication gate. Binding precedes creation of its runtime children.
// An unbound populated cache is retained and requires explicit owner review.
func OpenHost(ctx context.Context, dir string, domain cachefs.Domain, gate PublicationGate) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.Validate(); err != nil {
		return nil, err
	}
	if gate == nil {
		return nil, runtimeDomainError("host runtime storage needs its explicit publication admission")
	}
	var store *Store
	err := gate(ctx, func() error {
		var err error
		store, err = openStoreDomain(ctx, dir, true, domain)
		return err
	})
	if err != nil {
		if store != nil {
			err = errors.Join(err, store.Close())
		}
		return nil, err
	}
	store.gate = gate
	return store, nil
}

// OpenHostReadOnly checks an existing host-bound store without creating a root,
// child, binding or sentinel. It cannot adopt an unbound legacy store.
func OpenHostReadOnly(ctx context.Context, dir string, domain cachefs.Domain) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.Validate(); err != nil {
		return nil, err
	}
	return openStoreDomain(ctx, dir, false, domain)
}

// OpenHostForPurge checks only an existing root and sentinel with the exact
// bound host owner. Missing children are allowed for an interrupted purge.
func OpenHostForPurge(ctx context.Context, dir string, domain cachefs.Domain) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.Validate(); err != nil {
		return nil, err
	}
	return openPurgeStore(ctx, dir, domain)
}

func runtimeDomainError(message string) error {
	return &proto.Error{Code: "cache_domain", Message: message, Hint: "Restore the recorded host roots or use disjoint storage before retrying. Preserve unknown and other-domain files."}
}

// checkDomain runs only under this Store's existing stable sentinel lock.
// An empty root with .lock alone has no populated namespace to adopt. Every
// later transaction compares the same exact tuple before touching objects.
func (s *Store) checkDomain(ctx context.Context, file *os.File, initialise bool) error {
	actual, found, err := cachefs.ReadDomainBinding(ctx, s.root, lockName, file)
	if err != nil {
		return err
	}
	if s.domain.IsZero() {
		if found {
			return runtimeDomainError("a fixture or laptop constructor cannot use host-bound runtime storage")
		}
		return nil
	}
	if err := s.domain.Validate(); err != nil {
		return err
	}
	if found {
		if actual != s.domain {
			return runtimeDomainError("the runtime store belongs to another host domain")
		}
		return nil
	}
	if !initialise || s.readOnly {
		return runtimeDomainError("the existing runtime store has no proven host-domain binding")
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() != lockName {
			return runtimeDomainError("unbound runtime storage contains legacy or unknown objects")
		}
	}
	_, err = cachefs.InitialiseDomainBinding(ctx, s.root, lockName, file, s.domain)
	return err
}
