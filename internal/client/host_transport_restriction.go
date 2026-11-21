package client

import (
	"context"
	"errors"
	"sync"
	"time"
)

type hostTransportRestrictionKey struct{}

type hostTransportRestrictionV1 struct {
	mu       sync.Mutex
	expected *hostTransportExpectation
	settings *settingsOperation
	tier     TierEntry
	closed   bool
	closeErr error
}

// RestrictSavedHostSourceV1 limits later dials and grants to this ACTUAL
// accepted connection's original route, saved Source and held local settings.
// Tier data only adds a comparison; it issues no OP, answer or authority.
// The caller closes the returned scope after its one logical action finishes.
func (c *Conn) RestrictSavedHostSourceV1(ctx context.Context) (context.Context, func() error, error) {
	if ctx == nil || c == nil {
		return nil, nil, hostStateRemovalError("the shell action has no original saved-host connection")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if ctx.Value(hostTransportRestrictionKey{}) != nil {
		return nil, nil, hostStateRemovalError("the shell action already has its original saved-host restriction")
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r := &hostTransportRestrictionV1{expected: &hostTransportExpectation{parent: c, stamp: c.hostTransport}}
	if err := r.expected.check(check, c.Host); err != nil {
		return nil, nil, err
	}
	settings, err := openSettingsOperation(check)
	if err != nil {
		return nil, nil, err
	}
	r.settings = settings
	fail := func(err error) (context.Context, func() error, error) { return nil, r.close, err }
	if err := settings.retainRequestLifetime(check, ctx); err != nil {
		return fail(err)
	}
	tiers, err := readTiersFile()
	if err != nil {
		return fail(err)
	}
	var ok bool
	r.tier, ok = tiers.Entry(c.Host.Name)
	if !ok || !r.tier.Tier.Valid() || r.tier.NeedsAnswer || r.tier.IDChanged || r.tier.HostID != c.HostID() {
		return fail(grantIdentityError(c.Host))
	}
	bound := context.WithValue(ctx, hostTransportRestrictionKey{}, r)
	if err := r.expected.check(bound, c.Host); err != nil {
		return fail(err)
	}
	return bound, r.close, nil
}

func restrictedHostDialV1(ctx context.Context, h Host, beforeStart func(context.Context, Transport) error) (*Conn, bool, error) {
	r, _ := ctx.Value(hostTransportRestrictionKey{}).(*hostTransportRestrictionV1)
	if r == nil {
		return nil, false, nil
	}
	if err := r.expected.check(ctx, h); err != nil {
		return nil, true, err
	}
	c, err := dialHostContinuationPrepared(ctx, r.expected.parent, beforeStart)
	return c, true, err
}

// The actual accepted parent remains the Source. The snapshot and tier only
// refuse changes before transport startup and before a real grant publication.
func checkHostRestrictionV1(ctx context.Context, h Host, stamp *hostTransportStamp) error {
	r, _ := ctx.Value(hostTransportRestrictionKey{}).(*hostTransportRestrictionV1)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.expected == nil || r.expected.stamp == nil || stamp == nil ||
		!sameHostTransportSource(r.expected.stamp.source, stamp.source) || !SameEntry(r.expected.stamp.host, h) {
		return hostStateRemovalError("the shell action's original saved-host restriction ended or changed")
	}
	if err := r.expected.stamp.checkConn(r.expected.parent); err != nil {
		return err
	}
	if err := r.settings.Revalidate(ctx); err != nil {
		return err
	}
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	entry, ok := tiers.Entry(h.Name)
	if !ok || entry.Name != r.tier.Name || entry.Tier != r.tier.Tier || entry.HostID != r.tier.HostID ||
		entry.Key != r.tier.Key || entry.IDChanged != r.tier.IDChanged || entry.NeedsAnswer != r.tier.NeedsAnswer {
		return hostStateRemovalError("the shell action's original host tier or key changed")
	}
	return ctx.Err()
}

func (r *hostTransportRestrictionV1) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.closeErr = errors.Join(r.closeErr, r.settings.Close())
	}
	return r.closeErr
}
