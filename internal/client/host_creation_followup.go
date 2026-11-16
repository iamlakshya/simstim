package client

import (
	"context"
	"sync/atomic"
)

type hostCreationCommittedSource struct {
	host   Host
	source hostStateSource
	op     *heldLock
	stage  *stageBinding
	used   atomic.Bool
}

// The SAME actual writer invokes this only AFTER its successful durable
// publication, while original membership/OP are still held. Failed capture is
// committed uncertainty, never a raw-result or fresh-observation substitute.
func recordHostCreationCommittedSource(ctx context.Context, o *HostStateObservation, change HostChange, hosts []Host) error {
	if o.creation == nil || change.Was != nil || change.Now == nil || !o.creation.bound {
		return hostStateRemovalError("creation commit has no original bound result")
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	held, _ := ctx.Value(heldKey{o.name}).(*heldLock)
	if held == nil || held != o.creation.op.Load() {
		return hostStateRemovalError("creation commit lost its original native OP")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return err
	}
	source, err := captureHostStateSource(ctx, hosts, false)
	if err != nil {
		return err
	}
	if !source.exists {
		return hostStateRemovalError("creation publication has no actual committed Source")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.creation.committed != nil {
		return hostStateRemovalError("creation commit Source was already recorded")
	}
	o.creation.committed = &hostCreationCommittedSource{host: cloneObservationHost(*change.Now), source: source, op: held, stage: o.stage}
	return nil
}

// ContinueHostCreationContext consumes only the original actual writer's
// committed Source once. It retains the SAME physical OP for caller setup,
// ends creation authority, and supplies only an unchecked local read context.
// Actual registration/Dial must still observe its owned stage/Hello; this API
// never grants checked HostID, state mutation or remote/fresh source authority.
func ContinueHostCreationContext(ctx context.Context) (context.Context, error) {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil {
		return ctx, hostStateRemovalError("setup continuation has no original creator context")
	}
	o.mu.Lock()
	proof := o.creation.committed
	o.mu.Unlock()
	if proof == nil || !o.creation.finished.Load() {
		return ctx, hostStateRemovalError("setup continuation has no terminal successful creation commit")
	}
	held, _ := ctx.Value(heldKey{o.name}).(*heldLock)
	if held == nil || held != proof.op || held != o.creation.op.Load() || held.terminal.Load() {
		return ctx, hostStateRemovalError("setup continuation lost its original physical OP")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return ctx, err
	}
	if proof.stage == nil || proof.stage != o.stage {
		return ctx, hostStateRemovalError("setup continuation changed its original stage")
	}
	if err := sameStage(o.name, proof.stage.st); err != nil {
		return ctx, err
	}
	if err := withHostMembershipContext(ctx, proof.source.check); err != nil {
		return ctx, err
	}
	if !proof.used.CompareAndSwap(false, true) {
		return ctx, hostStateRemovalError("setup continuation already consumed its committed Source")
	}
	host := cloneObservationHost(proof.host)
	post := &HostStateObservation{role: hostStateRead, name: o.name, key: o.key, stateDir: o.stateDir, hostsPath: o.hostsPath, source: proof.source,
		original: &host, prospective: cloneObservationHost(host), op: held}
	return WithHostStateObservation(ctx, post), nil
}
