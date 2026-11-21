package supervisor

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/host"
	"time"
)

// acceptManagerCustodyBootstrapV1 touches no record or runtime until the
// authenticated SAMEOP final request consumes its inherited kernel claim.
func (s *Supervisor) acceptManagerCustodyBootstrapV1(ctx context.Context) (*host.Manifest, host.Manifest, error) {
	operation, err := s.awaitOriginalLoan(ctx)
	if err != nil {
		if s.bootOwner.ManagerCustodyPendingV1() {
			return nil, host.Manifest{}, errors.Join(host.ErrManagerCustodyPassiveAbortV1, err)
		}
		return nil, host.Manifest{}, err
	}
	s.bootOperation = operation
	if err := s.awaitManagerCustodyClaimV1(ctx); err != nil {
		if s.bootOwner.ManagerCustodyPendingV1() {
			return nil, host.Manifest{}, errors.Join(host.ErrManagerCustodyPassiveAbortV1, err)
		}
		return nil, host.Manifest{}, err
	}
	m, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, host.Manifest{}, err
	}
	s.mu.Lock()
	s.m = m
	snap := s.snapshot()
	s.mu.Unlock()
	return m, snap, nil
}

// awaitManagerCustodyClaimV1 owns the sole physical reader until final claim.
// Losing the outside requester leaves the one packet unconsumed, unless the
// actual claim already crossed its irreversible boundary. Both outcomes join
// this same bootstrap main; neither one starts a guest from saved metadata.
func (s *Supervisor) awaitManagerCustodyClaimV1(ctx context.Context) error {
	start := s.originalStart
	if start == nil || start.line == nil || s.bootOwner == nil {
		return execRefusal("the custody bootstrap has no original accepted operation line")
	}
	defer start.line.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { _ = start.line.SetDeadline(time.Now()) })
	defer stop()
	watchStop, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchStop:
				return
			case <-ctx.Done():
				_ = start.line.SetDeadline(time.Now())
				return
			case <-ticker.C:
			}
			if s.bootOwner.CheckManagerCustodyRequesterV1(ctx) != nil {
				_ = start.line.SetDeadline(time.Now())
				return
			}
		}
	}()
	defer func() { close(watchStop); <-watched }()
	_ = start.line.SetDeadline(time.Time{})
	for {
		// The initial listener already bounded an unauthenticated manual
		// activation. A genuine SAMEOP preparation has no time expiry. Its
		// real native requester remains observed even when no frame arrives.
		frame, err := start.line.ReadFrame()
		if err != nil {
			return err
		}
		claimContext, cancel := context.WithTimeout(ctx, systemGrace)
		committed, err := s.bootOwner.HandleManagerCustodyBootstrapV1(claimContext, start.line, frame)
		cancel()
		if err != nil {
			return err
		}
		if committed {
			return nil
		}
	}
}
