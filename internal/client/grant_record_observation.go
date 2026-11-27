package client

import "context"

// Ledger recording consumes an actual saved-connection observation. A
// committed-forget scope authorises tombstones, never a new credential record.
func validateGrantRecordObservation(ctx context.Context, h Host, hostID string) error {
	if ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(legacyStateWriteKey{}) != nil {
		return grantOperationError()
	}
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateSaved || o.name != h.Name || !SameEntry(o.prospective, h) {
		return grantOperationError()
	}
	o.mu.Lock()
	known := o.checked && ValidHostID(o.hostID) && o.hostID == hostID
	c := o.checkedConn
	legacy := hostID == "" && !o.checked && o.hostID == "" && o.legacyConn != nil
	if legacy {
		c = o.legacyConn
	}
	o.mu.Unlock()
	if (!known && !legacy) || c == nil || !SameEntry(c.Host, h) || c.HostID() != hostID {
		return grantIdentityError(h)
	}
	// Revalidate that exact Conn/private Proc/Hello/Source rather than
	// accepting caller arguments solely because a filename was permitted.
	return c.BindHostStateObservation(ctx)
}

func validateLegacyRecordArguments(ctx context.Context, h Host, hostID string, outbound Outbound) error {
	permit, _ := ctx.Value(legacyStateWriteKey{}).(*legacyStateWritePermit)
	if permit == nil {
		return nil
	}
	i := permit.intent
	if !permit.active.Load() || permit.consumed.Load() || i.conn == nil || hostID != "" ||
		!SameEntry(i.conn.Host, h) || i.outbound.Kind != outbound.Kind || i.outbound.Name != outbound.Name ||
		(i.kind != legacyNewCredentialRecord && i.kind != legacyResumeCredentialRecord && i.kind != legacyPasteRecord) {
		return grantOperationError()
	}
	return nil
}
