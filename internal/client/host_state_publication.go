package client

import "context"

// withHostPublicationObservation preserves the established metadata API. It
// grants only necessary OP creation and exact Was/Now membership publication.
// It grants no host JSON, remote observation or future producer authority.
func withHostPublicationObservation(ctx context.Context, c HostChange) (context.Context, func(), error) {
	if o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); o != nil {
		if err := o.checkOwner(ctx); err != nil {
			return ctx, nil, err
		}
		return ctx, func() {}, nil
	}
	if c.Now == nil {
		return ctx, func() {}, nil
	}
	st, err := stageForContext(ctx, c.Now.Name)
	if err != nil {
		return ctx, nil, err
	}
	if st != nil {
		return ctx, nil, hostStateRemovalError("the owned registration needs its original pre-observation token")
	}
	var observation *HostStateObservation
	err = withHostMembershipContext(ctx, func(locked context.Context) error {
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		if _, err := ApplyHostChange(hosts, c); err != nil {
			return err
		}
		source, err := captureHostStateSource(locked, hosts, true)
		if err != nil {
			return err
		}
		observation = &HostStateObservation{role: hostStatePublication, name: c.Now.Name, key: HostFileKey(c.Now.Name), stateDir: StateDir(),
			hostsPath: hostsPath(), source: source, prospective: cloneObservationHost(*c.Now)}
		if c.Was != nil {
			original := cloneObservationHost(*c.Was)
			observation.original = &original
		}
		observation.publication.Store(true)
		return nil
	})
	if err != nil {
		return ctx, nil, err
	}
	return WithHostStateObservation(ctx, observation), func() { observation.publication.Store(false) }, nil
}

func checkHostPublicationObservation(ctx context.Context, c HostChange) error {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil {
		return nil
	}
	if err := o.checkMembership(ctx); err != nil {
		return err
	}
	if o.name != c.name() || (o.original == nil) != (c.Was == nil) || o.original != nil && !SameEntry(*o.original, *c.Was) {
		return hostStateRemovalError("the metadata publication changed its observed original")
	}
	if o.role == hostStateCreation {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.creation == nil || !o.creation.bound || c.Was != nil || c.Now == nil || !SameEntry(o.prospective, *c.Now) {
			return hostStateRemovalError("the metadata CAS has no exact bound creation result")
		}
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	expected := o.prospective
	if o.publicationResult != nil {
		expected = *o.publicationResult
	}
	if c.Now != nil && !SameEntry(expected, *c.Now) {
		return hostStateRemovalError("the metadata publication changed its actual intended host")
	}
	return nil
}
