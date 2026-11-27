package client

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Only private original joined-source constructors can mark this restriction.
// It is not implemented by Host/manifest data or exported provider interfaces.
type joinedProjectionMarkerV1 interface{ joinedProjectionV1() }
type joinedProjectionSourceV1 struct{ route, original Host }

func (*joinedProjectionSourceV1) joinedProjectionV1() {}
func (s *joinedProjectionSourceV1) OpenSSHNativeSource(ctx context.Context) (SSHNativeSourceLease, error) {
	if s == nil || s.original.JoinedSSH == nil || !SameEntry(s.route, s.original) {
		return nil, hostStateRemovalError("the joined projection has no original writer reference")
	}
	var source hostStateSource
	var hostID string
	var registration *HostStateObservation
	err := withHostMembershipContext(ctx, func(locked context.Context) error {
		if observed, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); observed != nil && observed.role == hostStateRegistration {
			if !SameEntry(observed.prospective, s.original) {
				return hostStateRemovalError("the joined projection changed its original registration")
			}
			if err := observed.checkMembership(locked); err != nil {
				return err
			}
			registration = observed
			source = observed.source
			hostID = observed.expectedID
			return nil
		}
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == s.original.Name })
		if index < 0 || !SameEntry(hosts[index], s.original) {
			return hostStateRemovalError("the joined projection is not the actual saved entry")
		}
		if attempt, _ := ctx.Value(hostTransportAttemptKey{}).(*hostTransportAttempt); attempt != nil {
			if attempt.kind != hostTransportSaved || !SameEntry(attempt.host, s.original) {
				return hostStateRemovalError("the joined projection cannot adopt another transport role")
			}
			if attempt.continuation != nil {
				if err = attempt.continuation.check(locked, s.original); err != nil {
					return err
				}
				source = attempt.continuation.stamp.source
			} else if attempt.observation != nil {
				source = attempt.observation.source
			} else {
				return hostStateRemovalError("the joined projection has no original transport Source")
			}
			if err = source.check(locked); err != nil {
				return err
			}
		} else {
			source, err = captureHostStateSource(locked, hosts, false)
			if err != nil {
				return err
			}
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		entry, ok := tiers.Entry(s.original.Name)
		if !ok || entry.IDChanged || !ValidHostID(entry.HostID) {
			return grantIdentityError(s.original)
		}
		hostID = entry.HostID
		return nil
	})
	if err != nil {
		return nil, err
	}
	origin, err := openJoinedSSHOriginV1(s.original, hostID, false)
	if err != nil {
		return nil, err
	}
	restriction, err := openJoinedRouteRestrictionV1(ctx, origin.manifest.Route)
	if err != nil {
		return nil, errors.Join(err, origin.close())
	}
	lease := &joinedProjectionLeaseV1{expected: cloneObservationHost(s.original), source: source, hostID: hostID, registration: registration, origin: origin, routeRestriction: restriction}
	if err = lease.Revalidate(ctx); err != nil {
		return nil, errors.Join(err, lease.Close())
	}
	return lease, nil
}

type joinedProjectionLeaseV1 struct {
	mu               sync.Mutex
	expected         Host
	source           hostStateSource
	hostID           string
	registration     *HostStateObservation
	origin           *joinedSSHRemovalV1
	routeRestriction *joinedRouteRestrictionV1
	closed           bool
}

func (s *joinedProjectionLeaseV1) Revalidate(ctx context.Context) error {
	if s == nil {
		return hostStateRemovalError("the original joined projection lease is missing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return hostStateRemovalError("the original joined projection lease ended")
	}
	if err := withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := s.source.check(locked); err != nil {
			return err
		}
		if s.registration != nil {
			return s.registration.checkMembership(locked)
		}
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == s.expected.Name })
		if index < 0 || !SameEntry(hosts[index], s.expected) {
			return hostStateRemovalError("the original joined saved entry changed")
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		entry, ok := tiers.Entry(s.expected.Name)
		if !ok || entry.IDChanged || entry.HostID != s.hostID {
			return grantIdentityError(s.expected)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.origin.checkRetained(); err != nil {
		return err
	}
	return s.routeRestriction.check(ctx)
}
func (s *joinedProjectionLeaseV1) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	err := errors.Join(s.origin.close(), s.routeRestriction.close())
	if err == nil {
		s.closed = true
	}
	return err
}

func joinedProjectionWrapperAllowedV1(ctx context.Context, h Host) (bool, error) {
	scope, _ := ctx.Value(sshNativeSourceKey{}).(*sshNativeSourceScope)
	if scope == nil {
		return false, nil
	}
	if _, ok := scope.opener.(joinedProjectionMarkerV1); !ok {
		return false, nil
	}
	if !SameEntry(scope.route, h) {
		return false, hostStateRemovalError("the joined wrapper has another native route")
	}
	if err := scope.check(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// This check adds no source. Every pin/trust subprocess must retain the exact
// actual projection lease supplied by its original connection producer.
func checkJoinedProjectionStepV1(ctx context.Context, h Host) error {
	scope, _ := ctx.Value(sshNativeSourceKey{}).(*sshNativeSourceScope)
	if scope == nil {
		if h.JoinedSSH != nil {
			return hostStateRemovalError("joined SSH needs its actual retained native projection")
		}
		return ctx.Err()
	}
	if _, ok := scope.opener.(joinedProjectionMarkerV1); !ok {
		return ctx.Err()
	}
	if !SameEntry(scope.route, h) {
		return hostStateRemovalError("joined SSH step has another native route")
	}
	if scope.ctx.Err() != nil {
		return revalidateJoinedProjectionAfterStepV1(ctx, scope)
	}
	return scope.check(ctx)
}

func revalidateJoinedProjectionAfterStepV1(ctx context.Context, scope *sshNativeSourceScope) error {
	if scope == nil || !scope.active.Load() || scope.lease == nil {
		return hostStateRemovalError("the original native projection ended before its final check")
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(scope.ctx.Err(), scope.lease.Revalidate(bounded))
}
