package client

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

// SSHNativeSourceLease retains an actual provider's read-only native pins.
// It supplies no saved-host Source, grant, cleanup or privileged authority.
type SSHNativeSourceLease interface {
	Revalidate(context.Context) error
	Close() error
}

// SSHNativeSourceOpener is installed only by the actual provider transport
// constructor. Its lease adds a restriction to the existing *SSH identity.
type SSHNativeSourceOpener interface {
	OpenSSHNativeSource(context.Context) (SSHNativeSourceLease, error)
}

// WithNativeSource returns a same-type SSH transport with one immutable native
// restriction. It never replaces saved-host, pin, tier or consent checks.
func (s *SSH) WithNativeSource(opener SSHNativeSourceOpener) (*SSH, error) {
	if s == nil || s.nativeSource != nil || !sameHostRuntimeObject(opener, opener) {
		return nil, machineSourceUnknown()
	}
	copy := *s
	copy.Host = cloneObservationHost(s.Host)
	if s.origin != nil {
		original := cloneObservationHost(*s.origin)
		copy.origin = &original
	}
	copy.nativeSource = opener
	return &copy, nil
}

type sshNativeSourceKey struct{}
type sshNativeSourceScope struct {
	ctx             context.Context
	opener          SSHNativeSourceOpener
	lease           SSHNativeSourceLease
	route, original Host
	active          atomic.Bool
	once            sync.Once
	err             error
}

func (s *SSH) requiresNativeSource() bool {
	host := s.entryHost()
	return host.JoinedSSH != nil || host.Machine != nil && host.Machine.Provider == "lima" && host.Machine.Source != nil
}

func (scope *sshNativeSourceScope) matches(s *SSH) bool {
	if scope == nil || s == nil || !SameEntry(scope.route, s.Host) {
		return false
	}
	if s.nativeSource != nil {
		return sameHostRuntimeObject(scope.opener, s.nativeSource) && SameEntry(scope.original, s.entryHost())
	}
	// Existing pin readers construct an ordinary SSH value from this exact
	// checked route. The closed outer scope remains its native restriction.
	return true
}

func (scope *sshNativeSourceScope) check(ctx context.Context) error {
	if scope == nil || !scope.active.Load() || scope.lease == nil {
		return machineSourceUnknown()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scope.ctx.Err(); err != nil {
		return err
	}
	return scope.lease.Revalidate(ctx)
}

func (scope *sshNativeSourceScope) finish() error {
	scope.once.Do(func() {
		check := scope.check(scope.ctx)
		if _, joined := scope.opener.(joinedProjectionMarkerV1); joined {
			check = revalidateJoinedProjectionAfterStepV1(scope.ctx, scope)
		}
		scope.active.Store(false)
		scope.err = errors.Join(check, scope.lease.Close())
	})
	return scope.err
}

func (s *SSH) beginNativeSource(ctx context.Context) (context.Context, func() error, error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	if existing, _ := ctx.Value(sshNativeSourceKey{}).(*sshNativeSourceScope); existing != nil {
		if existing.matches(s) {
			if err := existing.check(ctx); err != nil {
				return ctx, nil, err
			}
			return ctx, func() error { return nil }, nil
		}
		if s.nativeSource != nil || s.requiresNativeSource() {
			return ctx, nil, machineSourceUnknown()
		}
	}
	opener := s.nativeSource
	if opener == nil && s.entryHost().JoinedSSH != nil {
		opener = &joinedProjectionSourceV1{route: cloneObservationHost(s.Host), original: cloneObservationHost(s.entryHost())}
	}
	if opener == nil {
		if s.requiresNativeSource() {
			return ctx, nil, machineSourceUnknown()
		}
		return ctx, func() error { return nil }, nil
	}
	lease, err := opener.OpenSSHNativeSource(ctx)
	if err != nil {
		if validSSHNativeLease(lease) {
			err = errors.Join(err, lease.Close())
		}
		return ctx, nil, err
	}
	if !validSSHNativeLease(lease) {
		return ctx, nil, machineSourceUnknown()
	}
	scope := &sshNativeSourceScope{ctx: ctx, opener: opener, lease: lease, route: cloneObservationHost(s.Host), original: cloneObservationHost(s.entryHost())}
	scope.active.Store(true)
	if err := scope.check(ctx); err != nil {
		return ctx, nil, errors.Join(err, scope.finish())
	}
	return context.WithValue(ctx, sshNativeSourceKey{}, scope), scope.finish, nil
}

func (s *SSH) checkNativeSourceScope(ctx context.Context) error {
	if scope, _ := ctx.Value(sshNativeSourceKey{}).(*sshNativeSourceScope); scope != nil && scope.matches(s) {
		return scope.check(ctx)
	}
	if s.nativeSource != nil || s.requiresNativeSource() {
		return machineSourceUnknown()
	}
	return ctx.Err()
}

func withMatchingNativeSource(target, owner context.Context) context.Context {
	if scope, _ := owner.Value(sshNativeSourceKey{}).(*sshNativeSourceScope); scope != nil {
		return context.WithValue(target, sshNativeSourceKey{}, scope)
	}
	return target
}

func validSSHNativeLease(lease SSHNativeSourceLease) bool {
	if lease == nil {
		return false
	}
	value := reflect.ValueOf(lease)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return !value.IsNil()
	default:
		return true
	}
}
