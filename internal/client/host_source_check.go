package client

import "context"

// CheckSavedSource checks this connection's original accepted saved-host
// Source. It creates no OP, value request, approval or replacement Source.
// A UI may reconnect for a NEW logical action after this check fails; an
// already selected request must retain its failure and original ownership.
func (c *Conn) CheckSavedSource(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return err
	}
	stamp := c.hostTransport
	if stamp.kind != hostTransportSaved || c.agentOnly {
		return grantOperationError()
	}
	return withHostMembershipContext(ctx, func(owned context.Context) error {
		if err := stamp.source.check(owned); err != nil {
			return err
		}
		file, err := loadHostsFile()
		if err != nil {
			return err
		}
		current, err := findPlacementHost(file.Hosts, c.Host.Name)
		if err != nil {
			return err
		}
		if !SameEntry(current, stamp.host) {
			return grantIdentityError(c.Host)
		}
		return stamp.source.check(owned)
	})
}
