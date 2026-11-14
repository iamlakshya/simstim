package client

import (
	"context"
	"slices"
	"sync"
)

// beginGrant holds the saved host stable until a request's first frame or
// the dedicated SSH process has been published. The caller releases it
// before waiting for a response, reading terminal input or fetching values.
func (c *Conn) beginGrant(ctx context.Context, kind string, sensitive bool) (context.Context, func(), error) {
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return ctx, nil, err
	}
	ctx, unlock, err := LockHost(ctx, c.Host.Name, kind)
	if err != nil {
		return ctx, nil, err
	}
	if err := checkTransportBeforeTierMigration(ctx, c); err != nil {
		unlock()
		return ctx, nil, err
	}
	if err := ensureTiersMigrated(ctx); err != nil {
		unlock()
		return ctx, nil, err
	}
	ctx, unpin, err := holdHostPublicationContext(ctx, c.Host.Name)
	if err != nil {
		unlock()
		return ctx, nil, err
	}
	var once sync.Once
	release := func() { once.Do(func() { unpin(); unlock() }) }
	if err := c.checkGrantHost(ctx, sensitive); err != nil {
		release()
		return ctx, nil, err
	}
	observation, err := ObserveHostStateContext(ctx, c.Host, c.HostID())
	if err != nil {
		release()
		return ctx, nil, err
	}
	ctx = WithHostStateObservation(ctx, observation)
	if err := c.BindHostStateObservation(ctx); err != nil {
		release()
		return ctx, nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		release()
		return ctx, nil, err
	}
	return ctx, release, nil
}

// Initial migration may mutate local metadata, so even this pre-publication
// step must match the accepted Conn's original Source under the actual OP.
func checkTransportBeforeTierMigration(ctx context.Context, c *Conn) error {
	if err := checkAcceptedHostTransport(c); err != nil {
		return err
	}
	stamp := c.hostTransport
	held, _ := ctx.Value(heldKey{c.Host.Name}).(*heldLock)
	if stamp.kind != hostTransportSaved || held == nil || !sameHostTransportSource(held.native.source, stamp.source) {
		return grantIdentityError(c.Host)
	}
	return withHostMembershipContext(ctx, stamp.source.check)
}

func (c *Conn) checkGrantHost(ctx context.Context, sensitive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == c.Host.Name })
	if i < 0 || !SameEntry(c.Host, hosts[i]) {
		return grantIdentityError(c.Host)
	}
	// Migration must finish before this publication participant starts.
	// A deleted or unavailable tier file now refuses instead of mutating.
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	entry, ok := tiers.Entry(c.Host.Name)
	if !ok || entry.HostID != "" && entry.HostID != c.HostID() {
		return grantIdentityError(c.Host)
	}
	if sensitive && (c.HostID() == "" || entry.HostID == "" || c.HostIDChanged() || entry.IDChanged) {
		return grantIdentityError(c.Host)
	}
	return nil
}
