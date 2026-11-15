package client

import (
	"context"
	"slices"
)

// This is an immutable restriction from an ACTUAL accepted ordinary parent.
// It owns no OP, observation, role promotion, tier answer or mutation permit.
type hostTransportExpectation struct {
	parent *Conn
	stamp  *hostTransportStamp
}

// dialHostContinuation retains the original accepted route and Source while
// starting one dedicated value-free connection. All mutation OPs end before
// network startup. The original Source is checked immediately before Start,
// and a new short actual OP revalidates it after the exact Hello arrives.
func dialHostContinuation(ctx context.Context, parent *Conn) (*Conn, error) {
	if ctx == nil || parent == nil || parent.agentOnly || parent.hostTransport == nil ||
		parent.hostTransport.kind != hostTransportSaved || !ValidHostID(parent.HostID()) || parent.HostIDChanged() {
		return nil, sandboxReceiptError()
	}
	if ctx.Value(heldKey{parent.Host.Name}) != nil || ctx.Value(hostStateObservationKey{}) != nil ||
		ctx.Value(hostTransportAttemptKey{}) != nil || ctx.Value(hostMembershipContextKey{}) != nil ||
		ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil ||
		ctx.Value(hostStateInspectionKey{}) != nil || ctx.Value(hostTerminalInspectionKey{}) != nil {
		return nil, hostStateRemovalError("a dedicated continuation must start from its original unheld caller context")
	}
	// Validate the original native OP/source, then RELEASE that context.
	// It is never carried into the transport or reused after unlock.
	_, release, err := parent.beginGrant(ctx, "prepare continuation", true)
	if err != nil {
		return nil, err
	}
	expected := &hostTransportExpectation{parent: parent, stamp: parent.hostTransport}
	release()
	if err := expected.check(ctx, parent.Host); err != nil {
		return nil, err
	}
	startup, cancel := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-parent.Done():
			cancel()
		case <-startup.Done():
		}
	}()
	defer func() { cancel(); <-watchDone }()
	attempt := &hostTransportAttempt{kind: hostTransportSaved, host: expected.stamp.host, priorID: parent.HostID(), continuation: expected}
	attempt.active.Store(true)
	defer attempt.active.Store(false)
	bound := context.WithValue(startup, hostTransportAttemptKey{}, attempt)
	route := parent.route
	route.lc = nil
	return dialObserved(bound, ctx, parent.Host, route, attempt)
}

func (e *hostTransportExpectation) check(ctx context.Context, h Host) error {
	if e == nil || e.parent == nil || e.stamp == nil || e.parent.hostTransport != e.stamp || e.stamp.kind != hostTransportSaved ||
		e.stamp.agentOnly || !ValidHostID(e.stamp.hostID) || e.stamp.idChanged || !SameEntry(e.stamp.host, h) {
		return hostStateRemovalError("the continuation has no original accepted saved-host expectation")
	}
	if err := e.stamp.checkConn(e.parent); err != nil {
		return err
	}
	// Existing canonical membership acquisition lasts only for this check.
	// No host OP or released heldKey is required or manufactured here.
	return withHostMembershipContext(ctx, e.stamp.source.check)
}

func finishHostContinuationObservation(ctx context.Context, attempt *hostTransportAttempt, child *Conn) error {
	e := attempt.continuation
	if err := e.check(ctx, child.Host); err != nil {
		return err
	}
	if child.agentOnly || child.HostID() != e.stamp.hostID || child.HostIDChanged() || child.hostTransport != nil {
		return grantIdentityError(child.Host)
	}
	// Only the ACTUAL original parent can enter this short checked OP. It
	// compares the original native root/file refs and membership Source;
	// current observation data can restrict, never refresh the expectation.
	owned, release, err := e.parent.beginGrant(ctx, "accept continuation", true)
	if err != nil {
		return err
	}
	defer release()
	if err := e.check(owned, child.Host); err != nil {
		return err
	}
	if !attempt.finished.CompareAndSwap(false, true) {
		return sandboxReceiptError()
	}
	child.hostTransport = &hostTransportStamp{kind: hostTransportSaved, host: e.stamp.host, source: e.stamp.source,
		stateDir: e.stamp.stateDir, hostsPath: e.stamp.hostsPath, parent: e.stamp.parent, op: e.stamp.op,
		conn: child, proc: child.proc, transport: child.route.t, hostID: child.HostID(), priorID: e.stamp.hostID,
		idChanged: false, agentOnly: false, features: slices.Clone(child.Features)}
	return nil
}
