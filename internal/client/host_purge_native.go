package client

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlakshya/simstim/internal/api"
)

// ResumeHostPurge selects only an existing completed host-stage receipt under
// the original local OP and membership Source. It creates no helper or Conn.
// A new native route is constrained to fixed cleanup continuations.
func ResumeHostPurge(ctx context.Context, h Host, force bool, mode RootStepMode, passwordless bool) (found bool, returned error) {
	r := grantRequestOf(ctx)
	if r == nil {
		return false, grantOperationError()
	}
	r.mu.Lock()
	valid := r.purpose == grantPurge && r.operation == grantOpPurge && r.purge == nil
	r.mu.Unlock()
	if !valid {
		return false, grantOperationError()
	}
	observation, err := ObserveHostStateContext(ctx, h, "")
	if err != nil {
		return false, err
	}
	ctx = WithHostStateObservation(ctx, observation)
	var record purgeRemovalReceiptV1
	err = withStateFile(ctx, []string{"hosts", HostFileKey(h.Name)}, "removal.json", false, func(raw []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, nil
		}
		var err error
		record, err = decodePurgeRemovalV1(raw, h)
		if err != nil {
			return nil, err
		}
		found = true
		return nil, nil
	})
	if err != nil || !found {
		return found, err
	}
	actual := hostStateSource{exists: record.Source.Exists, generation: record.Source.Generation, path: record.Source.Path, root: record.Source.Root}
	if !sameHostTransportSource(actual, observation.source) {
		return true, grantOperationError()
	}
	if err := withHostMembershipContext(ctx, actual.check); err != nil {
		return true, err
	}
	record, err = purgeRetryDecisionsV1(record, force, mode, passwordless)
	if err != nil {
		return true, err
	}
	if err := bindHostStateOwnerReceipt(ctx, h, record.HostID); err != nil {
		return true, err
	}
	native, endNative, attempt, err := beginHostTransportObservation(WithoutWake(WithoutInstall(ctx)), h)
	if err != nil {
		return true, err
	}
	owned := true
	defer func() {
		if owned {
			endNative()
		}
	}()
	route, err := routeTo(native, h, false)
	if err != nil {
		return true, err
	}
	if err := pinRoute(native, h, route.t, false); err != nil {
		return true, err
	}
	transport, _ := route.t.(*SSH)
	if route.t == nil {
		return true, hostStateRemovalError("the fixed cleanup continuation lost its actual native route")
	}
	if err := checkHostTransportAttempt(native, attempt, h); err != nil {
		return true, err
	}
	p := &purgeOperationV1{request: r, source: record.Source, observation: observation, receipt: record, rootMode: record.RootMode, rootPasswordless: record.Passwordless, rootConfigured: true, active: true, nativeTransport: transport, nativeContinuation: route.t, nativeContext: native, nativeAttempt: attempt, nativeFinish: endNative}
	r.mu.Lock()
	if r.purge != nil || r.lifetimeErrorLocked() != nil {
		r.mu.Unlock()
		return true, grantOperationError()
	}
	r.purge = p
	r.mu.Unlock()
	owned = false
	return true, p.check(ctx, nil)
}

func (p *purgeOperationV1) fixedTransport(ctx context.Context) (*SSH, context.Context, error) {
	if err := p.check(ctx, p.conn); err != nil {
		return nil, ctx, err
	}
	if p.conn != nil {
		transport, ok := p.conn.route.t.(*SSH)
		if !ok {
			return nil, ctx, hostStateRemovalError("the fixed cleanup continuation needs its original native SSH route")
		}
		return transport, WithHostStateObservation(ctx, p.observation), nil
	}
	if p.nativeTransport == nil || p.nativeContext == nil {
		return nil, ctx, grantOperationError()
	}
	if err := p.nativeContext.Err(); err != nil {
		return nil, ctx, errors.Join(ctx.Err(), err)
	}
	return p.nativeTransport, p.nativeContext, nil
}

func (p *purgeOperationV1) bindConnectionV1(ctx context.Context, c *Conn, force bool, mode RootStepMode, passwordless bool) (api.PurgeResult, error) {
	if p == nil || c == nil {
		return api.PurgeResult{}, grantOperationError()
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if p.conn != nil || !p.active || !SameEntry(record.Original, c.Host) || record.HostID != c.HostID() || record.Force != force || record.RootMode != mode || record.Passwordless != passwordless {
		return record.Report, grantOperationError()
	}
	owned, release, err := c.beginGrantSource(ctx, "resume exact host purge", true)
	if err != nil {
		return record.Report, err
	}
	source, err := captureHostStateSourceWitness(owned, c)
	observation, _ := owned.Value(hostStateObservationKey{}).(*HostStateObservation)
	release()
	if err != nil {
		return record.Report, err
	}
	if !sameReceiptSource(source, p.source) {
		return record.Report, grantOperationError()
	}
	p.mu.Lock()
	finish := p.nativeFinish
	p.nativeFinish = nil
	p.nativeTransport = nil
	p.nativeContinuation = nil
	p.nativeContext = nil
	p.nativeAttempt = nil
	p.conn = c
	p.observation = observation
	p.mu.Unlock()
	if finish != nil {
		finish()
	}
	return record.Report, p.check(ctx, c)
}

// ContinuePurgeHostStage first reads the closed native terminal role. If the
// original host stage has not reached it, only the existing helper may resume.
// Every transport remains constrained to the same stored Source and local OP.
func ContinuePurgeHostStage(ctx context.Context) (api.PurgeResult, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return api.PurgeResult{}, err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if record.has("host") {
		return record.Report, nil
	}
	// The front end has now completed typed consent. Persist a new explicit
	// work-loss decision before any native host-stage continuation.
	if err := p.save(ctx); err != nil {
		return record.Report, err
	}
	raw, recoverErr := p.runPurgePayload(ctx, "report", nil)
	if recoverErr == nil {
		var report api.PurgeResult
		if !api.ExactJSONFields(raw, report) || json.Unmarshal(raw, &report) != nil || !report.HostComplete || report.HostID != record.HostID || report.Nonce != record.Nonce || report.GuardWitness == nil || !report.GuardWitness.Prepared() {
			return record.Report, hostStateRemovalError("the recovered purge report lost its exact original completion witness")
		}
		if err := validatePurgeHostResultV1(report, record.HostID, record.Nonce); err != nil {
			return record.Report, err
		}
		p.mu.Lock()
		p.receipt.Report = report
		p.mu.Unlock()
		return report, p.finish(ctx, "host")
	}
	if err := ctx.Err(); err != nil {
		return record.Report, errors.Join(err, recoverErr)
	}
	c, err := Dial(WithoutWake(WithoutInstall(ctx)), record.Original, false)
	if err != nil {
		return record.Report, errors.Join(recoverErr, err)
	}
	if _, err := c.PreparePurge(ctx, record.Force, record.RootMode, record.Passwordless); err != nil {
		c.Close()
		return record.Report, errors.Join(recoverErr, err)
	}
	return c.Purge(ctx, record.Force)
}

// The read-only and fixed cleanup roles use the actual retained factory route,
// including SDK transports whose own Start/Wait retain their native lifetime.
// Root execution has a separate SSH-only consumer through fixedTransport.
func (p *purgeOperationV1) fixedContinuationTransportV1(ctx context.Context) (Transport, context.Context, error) {
	if err := p.check(ctx, p.conn); err != nil {
		return nil, ctx, err
	}
	if p.conn != nil {
		if p.conn.route.t == nil {
			return nil, ctx, grantOperationError()
		}
		return p.conn.route.t, WithHostStateObservation(ctx, p.observation), nil
	}
	if p.nativeContinuation == nil || p.nativeContext == nil {
		return nil, ctx, grantOperationError()
	}
	return p.nativeContinuation, p.nativeContext, nil
}
