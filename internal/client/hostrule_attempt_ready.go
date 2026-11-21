package client

import "context"

// CheckReadyV1 is the direct boundary after frontend questions. A skipped
// question callback, a partial Dial or an unfinished Auto facts read cannot
// make the actual request ready. This method never dials or selects values.
func (a *HostAttemptV1) CheckReadyV1(ctx context.Context) error {
	if a == nil || a.plan == nil || ctx == nil {
		return grantOperationError()
	}
	p := a.plan
	p.mu.Lock()
	a.mu.Lock()
	valid := p.active == a && !p.closed && p.selection != nil && p.ctx != nil && a.ctx != nil &&
		a.ready && !a.closing && !a.closed && !a.started && a.running == nil && a.request != nil && a.conn != nil && a.dial != nil
	original, request, conn, dial, candidate, host := a.ctx, a.request, a.conn, a.dial, a.candidate, a.host
	a.mu.Unlock()
	p.mu.Unlock()
	if !valid || dial.connection() != conn || !SameEntry(conn.Host, host) {
		return grantOperationError()
	}
	if before := grantRequestOf(ctx); before != nil && before != request {
		return grantOperationError()
	}
	call, end, err := narrowHostAttemptContextV1(original, ctx)
	if err != nil {
		return err
	}
	defer end()
	request.mu.Lock()
	valid = request.hostAttempt == a && request.hostSelection == p.selection && request.selectedHost == host.Name &&
		request.purpose == grantRemote && request.operation == grantOpNew && request.operationUntrusted == request.Untrusted &&
		request.Untrusted == p.need.Untrusted && request.SSHAgent == p.args.SSHAgent && !request.ending && !request.preparationClosed
	request.mu.Unlock()
	if !valid || grantRequestOf(call) != request {
		return grantOperationError()
	}
	if err := p.selection.Check(call); err != nil {
		return err
	}
	if err := conn.CheckSavedSource(call); err != nil {
		return err
	}
	if err := checkAutoNewReadyV1(call, request); err != nil {
		return err
	}
	if p.automatic != nil {
		if candidate == nil {
			return grantOperationError()
		}
		return p.automatic.checkCandidateV1(call, candidate)
	}
	return call.Err()
}
