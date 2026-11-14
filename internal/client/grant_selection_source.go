package client

import "context"

// A selection Source is an immutable local fact, not checked remote identity
// or a state-write capability. It survives runtime retries without refreshing
// an old request, terminal answer or value payload after membership ABA.
type grantSelectionSource struct {
	host   string
	source hostStateSource
}

func checkAcceptedHostTransport(c *Conn) error {
	if c == nil || c.hostTransport == nil {
		return hostStateRemovalError("value selection needs the original accepted transport observation")
	}
	return c.hostTransport.checkConn(c)
}

func bindRequestTransportSource(ctx context.Context, c *Conn) error {
	if err := checkAcceptedHostTransport(c); err != nil {
		return err
	}
	stamp := c.hostTransport
	if stamp.kind != hostTransportSaved || stamp.hostID == "" && stamp.priorID != "" {
		return grantIdentityError(c.Host)
	}
	current, err := captureHostSelectionSource(ctx, c.Host)
	if err != nil {
		return err
	}
	if !sameHostTransportSource(stamp.source, current) {
		return grantOperationError()
	}
	return bindGrantSelectionSource(ctx, c.Host.Name, current)
}

func bindRequestLocalSource(ctx context.Context, h Host) error {
	source, err := captureHostSelectionSource(ctx, h)
	if err != nil {
		return err
	}
	return bindGrantSelectionSource(ctx, h.Name, source)
}

func bindGrantSelectionSource(ctx context.Context, host string, source hostStateSource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.selection == nil {
		r.selection = &grantSelectionSource{host: host, source: source}
		return nil
	}
	if r.selection.host != host || !sameHostTransportSource(r.selection.source, source) {
		return grantOperationError()
	}
	return nil
}
