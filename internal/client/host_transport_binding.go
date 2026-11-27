package client

import "context"

// BindHostStateObservation is the one public consumer of the immutable actual
// Dial stamp. A checked ID and an unchecked empty-ID observation stay distinct.
func (c *Conn) BindHostStateObservation(ctx context.Context) error {
	if c == nil {
		return hostStateRemovalError("the connection has no actual transport observation")
	}
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	return bindHostStateIdentity(ctx, o, c.Host, c.HostID(), c)
}

func bindHostStateIdentity(ctx context.Context, o *HostStateObservation, h Host, hostID string, conn *Conn) error {
	if err := o.checkOwner(ctx); err != nil {
		return err
	}
	if o.role != hostStateSaved && o.role != hostStateRegistration {
		return hostStateRemovalError("this local role has no remote observation authority")
	}
	if !SameEntry(h, o.prospective) {
		return hostStateRemovalError("the accepted hello belongs to another state observation")
	}
	var stamp *hostTransportStamp
	if conn != nil {
		stamp = conn.hostTransport
		if stamp == nil {
			return hostStateRemovalError("the connection has no original actual Dial Source stamp")
		}
		if err := stamp.checkConn(conn); err != nil {
			return err
		}
		if !sameHostTransportSource(stamp.source, o.source) {
			return hostStateRemovalError("the connection belongs to an older host membership Source")
		}
		if hostID == "" && o.role == hostStateSaved {
			// A post-H4 empty tier record cannot erase either the pre-Dial
			// expected known ID or a later current saved known ID.
			currentID, err := transportPriorHostID(ctx, o, h)
			if err != nil {
				return err
			}
			if currentID != "" {
				return grantIdentityError(h)
			}
		}
	}
	if err := withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := o.checkMembership(locked); err != nil {
			return err
		}
		if stamp != nil {
			return stamp.checkObservation(locked, o, conn)
		}
		return nil
	}); err != nil {
		return err
	}
	o.mu.Lock()
	if hostID == "" {
		if conn == nil || stamp == nil || stamp.kind != hostTransportSaved || stamp.agentOnly || o.role != hostStateSaved ||
			o.expectedID != "" || o.hostID != "" || o.checked || o.checkedConn != nil ||
			o.legacyConn != nil && o.legacyConn != conn || o.transport != nil && o.transport != stamp {
			o.mu.Unlock()
			return hostStateRemovalError("the unchecked hello has no matching ordinary saved-host observation")
		}
		o.legacyConn, o.transport = conn, stamp
		o.mu.Unlock()
		return o.checkOwner(ctx)
	}
	if !ValidHostID(hostID) || o.expectedID != "" && o.expectedID != hostID || o.hostID != "" && o.hostID != hostID ||
		o.legacyConn != nil || o.checked && o.checkedConn != conn || o.transport != nil && o.transport != stamp {
		o.mu.Unlock()
		return hostStateRemovalError("the checked host identity or connection changed after the original observation")
	}
	o.hostID, o.checked, o.checkedConn, o.transport = hostID, true, conn, stamp
	o.mu.Unlock()
	return o.checkOwner(ctx)
}

func (o *HostStateObservation) legacyTransport(ctx context.Context) (*Conn, error) {
	if err := o.checkOwner(ctx); err != nil {
		return nil, err
	}
	o.mu.Lock()
	conn, stamp := o.legacyConn, o.transport
	checked := o.checked || o.hostID != "" || o.checkedConn != nil || o.expectedID != ""
	o.mu.Unlock()
	if checked || o.role != hostStateSaved || conn == nil || stamp == nil || stamp.kind != hostTransportSaved || stamp.agentOnly || stamp.hostID != "" || stamp.priorID != "" ||
		!sameHostTransportSource(stamp.source, o.source) {
		return nil, hostStateRemovalError("the observation has no original unchecked legacy transport class")
	}
	if err := stamp.checkConn(conn); err != nil {
		return nil, err
	}
	return conn, nil
}

func (o *HostStateObservation) hasLegacyBinding() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.legacyConn != nil
}
