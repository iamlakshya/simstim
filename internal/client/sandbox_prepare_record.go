package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/wall"
)

// RecordSandboxPreparationV1 publishes Host.Prepared and Host.WallFloor for
// target through the canonical UpdateHosts writer CAS. It succeeds ONLY after
// the same original successful wall test boot and positive owned cleanup under
// unchanged membership Source, host identity, pinned host key, tier and
// settings. Public SandboxPrepareResultV1 fields never authorise this write.
func RecordSandboxPreparationV1(ctx context.Context, target *HostPrepareTargetV1, outcome *SandboxPrepareOutcomeV1) (Host, error) {
	if ctx == nil {
		return Host{}, sandboxPrepareError("recording sandbox preparation needs an active context")
	}
	if err := ctx.Err(); err != nil {
		return Host{}, err
	}
	if err := refuseNestedHostPrepareScope(ctx); err != nil {
		return Host{}, err
	}
	if err := requireHostPrepareOperation(ctx); err != nil {
		return Host{}, err
	}
	if err := target.checkValid(); err != nil {
		return Host{}, err
	}
	if ctx.Value(heldKey{target.host.Name}) != nil || ctx.Value(hostStateObservationKey{}) != nil ||
		ctx.Value(hostTransportAttemptKey{}) != nil || ctx.Value(stageBindingKey{}) != nil {
		return Host{}, hostStateRemovalError("recording sandbox preparation must start from a clean unheld caller context")
	}
	if outcome == nil || outcome.plan == nil || !outcome.tested || outcome.noRunnableWall {
		return Host{}, sandboxPrepareError("recording a sandbox floor requires a completed successful wall test boot and cleanup")
	}
	r := grantRequestOf(ctx)
	plan := outcome.plan
	r.mu.Lock()
	owned := r.sandboxPrepare == plan
	r.mu.Unlock()
	if !owned {
		return Host{}, grantOperationError()
	}

	plan.mu.Lock()
	ready := plan.request == r && plan.target == target && plan.conn == outcome.conn && plan.stamp == outcome.stamp &&
		!plan.closed && !plan.closing && !plan.busy && !plan.imagePhaseActive && !plan.recording && !plan.recorded &&
		plan.completed && plan.sent && plan.wrote && plan.replied && !plan.failed && !plan.uncertain &&
		plan.bootSucceeded && plan.cleanupConfirmed && !plan.noRunnableWall && plan.operation != nil
	if !ready {
		plan.mu.Unlock()
		return Host{}, sandboxPrepareError("this preparation outcome cannot authorise a floor or cache write")
	}
	plan.recording = true
	level := plan.level
	res := plan.result
	wantDriver := plan.expectedDriver
	probe := plan.probe
	afterRoot := plan.levelAfterRoot
	runtimeName := plan.runtimeName
	hostID := plan.hostID
	conn := plan.conn
	stamp := plan.stamp
	operation := plan.operation
	plan.mu.Unlock()
	defer func() {
		plan.mu.Lock()
		plan.recording = false
		plan.mu.Unlock()
	}()

	if conn == nil || stamp == nil || conn.hostTransport != stamp || stamp.kind != hostTransportSaved ||
		conn.HostIDChanged() || conn.HostID() != hostID || !ValidHostID(hostID) ||
		!SameEntry(target.host, stamp.host) || !sameHostTransportSource(target.source, stamp.source) {
		return Host{}, grantIdentityError(target.host)
	}
	testedLevel, err := wall.ParseLevel(level)
	if err != nil || testedLevel < wall.Process || testedLevel > wall.MicroVM {
		return Host{}, sandboxPrepareError("sandbox preparation did not test a valid runnable wall level")
	}
	if res.Version != 1 || res.Level != level || !validSandboxPrepareDriver(level, wantDriver, res.Driver) || res.BootMS < 0 {
		return Host{}, sandboxPrepareProtocolError("the recorded sandbox preparation result is incomplete or invalid")
	}
	if err := operation.Revalidate(ctx); err != nil {
		return Host{}, err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return Host{}, err
	}
	if err := ValidateSettings(settings); err != nil {
		return Host{}, err
	}
	if err := target.checkCurrent(ctx, hostID); err != nil {
		return Host{}, err
	}
	if err := allowSandboxPrepareFloorV1(target, testedLevel.String()); err != nil {
		return Host{}, err
	}

	was := target.Host()
	newFloor, err := nextPreparedWallFloor(was.WallFloor, testedLevel)
	if err != nil {
		return Host{}, err
	}
	prepared := &PreparedHost{
		Level:      testedLevel.String(),
		Runtime:    runtimeName,
		Arch:       probe.Arch,
		MemTotal:   probe.MemTotal,
		PreparedAt: nowUTC(),
	}
	if afterLevel, err := wall.ParseLevel(afterRoot); err == nil && afterLevel > testedLevel {
		prepared.LevelAfterRoot = afterLevel.String()
	}
	now := cloneObservationHost(was)
	now.WallFloor = newFloor
	now.Prepared = prepared

	checkBeforeWrite := func(locked context.Context) error {
		if err := locked.Err(); err != nil {
			return err
		}
		if err := requireHostPrepareOperation(locked); err != nil {
			return err
		}
		if err := operation.Revalidate(locked); err != nil {
			return err
		}
		currentSettings, err := operation.Settings(locked)
		if err != nil {
			return err
		}
		if err := ValidateSettings(currentSettings); err != nil {
			return err
		}
		if err := target.checkLocked(locked, hostID); err != nil {
			return err
		}
		if err := allowSandboxPrepareFloorV1(target, testedLevel.String()); err != nil {
			return err
		}
		plan.mu.Lock()
		ok := !plan.closed && !plan.closing && !plan.recorded && plan.bootSucceeded && plan.cleanupConfirmed && !plan.failed && !plan.uncertain
		plan.mu.Unlock()
		if !ok {
			return sandboxPrepareError("the preparation outcome is no longer valid for publication")
		}
		return nil
	}

	pubCtx := context.WithValue(ctx, hostPrepareRecordCheckKey{}, checkBeforeWrite)
	if err := UpdateHosts(pubCtx, HostChange{Kind: "host prepare", Was: &was, Now: &now}); err != nil {
		return Host{}, err
	}
	plan.mu.Lock()
	plan.recorded = true
	plan.mu.Unlock()
	return cloneObservationHost(now), nil
}

// nextPreparedWallFloor establishes an absent floor or raises a weaker one,
// while preserving any stronger existing floor without explicit downgrade.
func nextPreparedWallFloor(current string, tested wall.Level) (string, error) {
	if tested < wall.Process || tested > wall.MicroVM {
		return "", sandboxPrepareError("a wall floor requires process, gvisor or microvm")
	}
	if current == "" {
		return tested.String(), nil
	}
	existing, err := ParseWallFloor(current)
	if err != nil {
		return "", err
	}
	if tested > existing {
		return tested.String(), nil
	}
	return existing.String(), nil
}
