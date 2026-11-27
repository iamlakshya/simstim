package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// PreparePurge retains this actual saved-host connection and local operation.
// A stored receipt is history; it cannot select a new host or value Source.
func (c *Conn) PreparePurge(ctx context.Context, force bool, mode RootStepMode, passwordless bool) (api.PurgeResult, error) {
	var result api.PurgeResult
	if _, err := ParseRootStepMode(string(mode)); err != nil {
		return result, err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return result, grantOperationError()
	}
	r.mu.Lock()
	existing := r.purge
	valid := r.purpose == grantPurge && r.operation == grantOpPurge
	r.mu.Unlock()
	if existing != nil {
		return existing.bindConnectionV1(ctx, c, force, mode, passwordless)
	}
	if !valid {
		return result, grantOperationError()
	}
	owned, release, err := c.beginGrantSource(ctx, "prepare host purge", true)
	if err != nil {
		return result, err
	}
	source, err := captureHostStateSourceWitness(owned, c)
	if err != nil {
		release()
		return result, err
	}
	observation, _ := owned.Value(hostStateObservationKey{}).(*HostStateObservation)
	release()
	nonce, err := grantRequestNonce()
	if err != nil {
		return result, err
	}
	record := purgeRemovalReceiptV1{Version: 2, Original: cloneObservationHost(c.Host), HostID: c.HostID(), Nonce: nonce, Source: source, Force: force, RootMode: mode, Passwordless: passwordless, Done: []string{}}
	stateCtx := WithHostStateObservation(ctx, observation)
	err = withStateFile(stateCtx, []string{"hosts", HostFileKey(c.Host.Name)}, "removal.json", false, func(raw []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, nil
		}
		saved, err := decodePurgeRemovalV1(raw, c.Host)
		if err != nil {
			return nil, err
		}
		if saved.HostID != c.HostID() || !sameReceiptSource(saved.Source, source) {
			return nil, hostStateRemovalError("the retry differs from its original host and Source")
		}
		saved, err = purgeRetryDecisionsV1(saved, force, mode, passwordless)
		if err != nil {
			return nil, err
		}
		record = saved
		return nil, nil
	})
	if err != nil {
		return result, err
	}
	p := &purgeOperationV1{request: r, conn: c, source: source, observation: observation, receipt: record, rootMode: mode, rootPasswordless: passwordless, rootConfigured: true, active: true}
	r.mu.Lock()
	if r.purge != nil || r.lifetimeErrorLocked() != nil {
		r.mu.Unlock()
		return result, grantOperationError()
	}
	r.purge = p
	r.mu.Unlock()
	if err := p.save(ctx); err != nil {
		return result, err
	}
	return record.Report, nil
}

// Purge uses the original removal nonce and typed local force decision. The
// response is saved before any root, login, baseline or forget continuation.
func (c *Conn) Purge(ctx context.Context, force bool) (api.PurgeResult, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return api.PurgeResult{}, err
	}
	if err := p.check(ctx, c); err != nil {
		return api.PurgeResult{}, err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if record.Force != force {
		return api.PurgeResult{}, grantOperationError()
	}
	release, err := p.retainNativeCallV1()
	if err != nil {
		return record.Report, err
	}
	defer release()

	if record.has("host") {
		return record.Report, nil
	}
	if !c.HasFeature("purge") {
		return record.Report, c.grantHelperOld("remove the host's installed resources")
	}
	id, ch, err := c.startCall(ctx, "host.purge", api.PurgeArgs{HostID: record.HostID, Nonce: record.Nonce, Force: force})
	if err != nil {
		return record.Report, err
	}
	var raw json.RawMessage
	requestErr := c.awaitCallResult(ctx, "host.purge", id, ch, &raw, true)
	var report api.PurgeResult
	if len(raw) != 0 {
		if !api.ExactJSONFields(raw, report) || json.Unmarshal(raw, &report) != nil || report.HostID != record.HostID || report.Nonce != record.Nonce {
			return record.Report, errors.Join(requestErr, hostStateRemovalError("the purge report differs from its original host and removal attempt"))
		}
		if err := validatePurgeHostResultV1(report, record.HostID, record.Nonce); err != nil {
			return record.Report, errors.Join(requestErr, err)
		}
		if err := p.check(ctx, c); err != nil {
			return record.Report, errors.Join(requestErr, err)
		}
		p.mu.Lock()
		p.receipt.Report = report
		p.mu.Unlock()
		if err := p.save(ctx); err != nil {
			return report, errors.Join(requestErr, err)
		}
	}
	if requestErr != nil {
		return report, requestErr
	}
	stages := []string{"precheck", "sessions", "footprints", "roots", "helpers", "host-complete"}
	if !report.HostComplete || !slices.Equal(report.Done, stages) || report.GuardWitness == nil || !report.GuardWitness.Prepared() || report.GuardWitness.HostID != record.HostID || report.GuardWitness.Nonce != record.Nonce {
		return report, hostStateRemovalError("the host purge ended without its complete original acknowledgement witness")
	}
	return report, p.finish(ctx, "host")
}

func purgeOperationOfV1(ctx context.Context) (*purgeOperationV1, error) {
	r := grantRequestOf(ctx)
	if r == nil {
		return nil, grantOperationError()
	}
	r.mu.Lock()
	p := r.purge
	r.mu.Unlock()
	if p == nil {
		return nil, grantOperationError()
	}
	return p, nil
}

// FinishPurgeRootUndo records only the real fixed executor's successful return.
func FinishPurgeRootUndo(ctx context.Context, mode RootStepMode, options RootRunOptions) error {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	done := p.receipt.has("root")
	p.mu.Unlock()
	if done {
		return nil
	}
	if err := RunPurgeRootUndo(ctx, mode, options); err != nil {
		return err
	}
	p.mu.Lock()
	p.receipt.RootUndoRan = p.rootUndoRan
	p.mu.Unlock()
	p.mu.Lock()
	selected := p.rootUndoSelected
	p.mu.Unlock()
	if mode == RootSkip || mode == RootPrint && selected {
		p.mu.Lock()
		reason := "the fixed root undo was printed for the host owner"
		if mode == RootSkip {
			reason = "the fixed root undo was explicitly skipped"
		}
		p.receipt.Report.Left = append(p.receipt.Report.Left, api.PurgeItem{Kind: "root-step", Reason: reason, Hint: "Run the printed fixed undo script as the machine owner to remove the remaining root changes."})
		p.mu.Unlock()
	}
	if p.rootUndoRan {
		p.mu.Lock()
		p.receipt.Report.Left = append(p.receipt.Report.Left, api.PurgeItem{Kind: "root-journal", Reason: "The fixed undo retained its helper files and any entries outside this laptop's original run records.", Hint: "Inspect the retained root journal on the host as its machine owner."})
		p.mu.Unlock()
	}
	return p.finish(ctx, "root")
}

func CheckPurgeBaseline(ctx context.Context) (BaselineChanges, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return BaselineChanges{}, err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if record.has("baseline") {
		return record.Baseline, nil
	}
	if !record.has("logins") {
		return BaselineChanges{}, grantOperationError()
	}
	var before baselineFileV1
	found := false
	stateCtx := WithHostStateObservation(ctx, p.observation)
	err = withStateFile(stateCtx, []string{"hosts", HostFileKey(record.Original.Name)}, "baseline.json", false, func(raw []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, nil
		}
		var err error
		before, err = decodeBaselineV1(raw, record.Original.Name, record.HostID)
		found = err == nil
		return nil, err
	})
	if err != nil {
		return BaselineChanges{}, err
	}
	changes := BaselineChanges{}
	if !found {
		changes.Unavailable = []string{"the original host-add baseline"}
	} else {
		raw, err := p.runFixedNative(ctx, baselineCommandV1, nil, 16<<10)
		if err != nil {
			return changes, err
		}
		after, err := parseBaselineV1(raw)
		if err != nil {
			return changes, err
		}
		changes = compareBaselineV1(before.Readings, after)
	}
	p.mu.Lock()
	p.receipt.Baseline = changes
	p.mu.Unlock()
	return changes, p.finish(ctx, "baseline")
}

func PurgeRotateNotice(ctx context.Context) ([]string, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if record.has("rotate") {
		return slices.Clone(record.Rotate), nil
	}
	if !record.has("baseline") {
		return nil, grantOperationError()
	}
	names, err := RotateSecretsContext(ctx, record.Original)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.receipt.Rotate = slices.Clone(names)
	p.mu.Unlock()
	return names, p.finish(ctx, "rotate")
}

func CommitHostPurge(ctx context.Context) error {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if record.has("guard") {
		return nil
	}
	if !record.readyForGuard() || record.Report.GuardWitness == nil {
		return grantOperationError()
	}
	if p.conn == nil {
		return p.commitNativeGuard(ctx, *record.Report.GuardWitness)
	}
	if err := p.conn.Call(ctx, "host.purge.commit", *record.Report.GuardWitness, nil); err != nil {
		return err
	}
	p.mu.Lock()
	p.receipt.Report.GuardRemoved = true
	p.mu.Unlock()
	return p.finish(ctx, "guard")
}

func ForgetPurgedHost(ctx context.Context) error {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if !record.complete() {
		return grantOperationError()
	}
	hooks := HostForgetReceiptHooks{Inspect: inspectPurgeRemovalV1}
	hooks.Prepare = func(preparing context.Context, target string) error {
		if err := requireHostForgetPreparation(preparing, record.Original, record.HostID, target); err != nil {
			return err
		}
		return withStateFile(preparing, []string{"hosts", HostFileKey(record.Original.Name)}, "removal.json", true, func(raw []byte, exists bool) ([]byte, error) {
			if !exists {
				return nil, hostStateRemovalError("the complete removal receipt disappeared before forget")
			}
			current, err := decodePurgeRemovalV1(raw, record.Original)
			if err != nil {
				return nil, err
			}
			if !current.complete() || current.Nonce != record.Nonce || !sameReceiptSource(current.Source, record.Source) {
				return nil, grantOperationError()
			}
			current.TargetGeneration = target
			return json.Marshal(current)
		})
	}
	hooks.Verify = func(verifying context.Context, raw []byte) (HostForgetMembershipProof, error) {
		current, err := decodePurgeRemovalV1(raw, record.Original)
		if err != nil {
			return HostForgetMembershipProof{}, err
		}
		if !current.complete() || current.Nonce != record.Nonce || current.TargetGeneration == "" || !sameReceiptSource(current.Source, record.Source) {
			return HostForgetMembershipProof{}, grantOperationError()
		}
		return HostForgetMembershipProof{FromGeneration: record.Source.Generation, TargetGeneration: current.TargetGeneration}, verifying.Err()
	}
	owned, cancel, err := WithHostForgetIntent(ctx, record.Original, record.HostID, hooks)
	if err != nil {
		return err
	}
	defer cancel()
	if err := p.close(); err != nil {
		return err
	}
	if err := UpdateHosts(owned, HostChange{Kind: "host rm", Was: &record.Original}); err != nil {
		return err
	}
	return WithHostForgetCommittedContext(owned, record.Original, func(verified context.Context) error {
		if err := MarkGrantsRemovedContext(verified, record.Original); err != nil {
			return err
		}
		result, err := RemoveHostStateContext(verified, record.Original, record.HostID, purgeLocalValidatorsV1(verified, record))
		if err != nil {
			return err
		}
		if len(result.Preserved) != 0 {
			return hostStateRemovalError("local host metadata was preserved for inspection after forgetting the host")
		}
		return nil
	})
}

// Same native Start, bounded output read, Wait and provider finish retain the
// original operation across the fixed baseline/login/ack continuations.
func (p *purgeOperationV1) runFixedNative(ctx context.Context, command string, input io.Reader, limit int64) (output []byte, returned error) {
	release, err := p.retainNativeCallV1()
	if err != nil {
		return nil, err
	}
	defer release()

	transport, owned, err := p.fixedContinuationTransportV1(ctx)
	if err != nil {
		return nil, err
	}
	native, finish := owned, func() error { return nil }
	if ssh, ok := transport.(*SSH); ok {
		native, finish, err = ssh.beginNativeSource(owned)
		if err != nil {
			return nil, err
		}
	}
	defer func() { returned = errors.Join(returned, finish()) }()
	if err := p.check(native, p.conn); err != nil {
		return nil, err
	}
	process, err := transport.Start(native, command)
	if err != nil {
		return nil, err
	}
	if input != nil {
		_, err = io.Copy(process.Stdin(), input)
	}
	closeErr := process.Stdin().Close()
	if err != nil || closeErr != nil {
		process.Kill()
		return nil, errors.Join(err, closeErr, process.Wait())
	}
	output, err = io.ReadAll(io.LimitReader(process.Stdout(), limit+1))
	if len(output) > int(limit) {
		process.Kill()
		err = hostStateRemovalError("the fixed host continuation exceeded its output bound")
	}
	waitErr := process.Wait()
	if err := p.check(native, p.conn); err != nil {
		return nil, errors.Join(err, waitErr)
	}
	return output, errors.Join(err, waitErr)
}

// PurgeRootDecision returns the original operation's recorded restriction.
// It issues no root execution or native source permission.
func PurgeRootDecision(ctx context.Context) (RootStepMode, bool, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return "", false, err
	}
	if err := p.check(ctx, p.conn); err != nil {
		return "", false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rootMode, p.rootPasswordless, nil
}

// PurgeOutcomeReport is a copy of acknowledged report data. A projection must
// omit its historical guard witness; this return value grants no permission.
func PurgeOutcomeReport(ctx context.Context) (api.PurgeResult, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return api.PurgeResult{}, err
	}
	if err := p.check(ctx, p.conn); err != nil {
		return api.PurgeResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.receipt.complete() {
		return api.PurgeResult{}, grantOperationError()
	}
	report := p.receipt.Report
	report.Done = slices.Clone(report.Done)
	report.Removed = slices.Clone(report.Removed)
	report.Left = slices.Clone(report.Left)
	report.Logins = slices.Clone(report.Logins)
	report.Unsaved = slices.Clone(report.Unsaved)
	report.GuardWitness = nil
	return report, nil
}

func PurgeRootExecution(ctx context.Context) (bool, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return false, err
	}
	if err := p.check(ctx, p.conn); err != nil {
		return false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.receipt.has("root") {
		return false, grantOperationError()
	}
	return p.receipt.RootUndoRan, nil
}

func PurgeHasRootUndoSteps(ctx context.Context) (bool, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return false, err
	}
	return hasPurgeRootUndoStepsV1(ctx, p)
}

func (p *purgeOperationV1) markRootDispatchV1(ctx context.Context) error {
	if err := p.check(ctx, p.conn); err != nil {
		return err
	}
	p.mu.Lock()
	if p.receipt.RootMode != RootRun || p.receipt.has("root") {
		p.mu.Unlock()
		return grantOperationError()
	}
	p.receipt.RootAttempted = true
	p.mu.Unlock()
	return p.save(ctx)
}
