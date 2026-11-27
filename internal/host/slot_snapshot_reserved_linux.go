//go:build linux

package host

import (
	"context"
	"errors"
	"runtime"

	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// This constructor consumes only the distinct actual accepted reservation
// lane. It cannot populate or reinterpret a legacy seventeen-role issuer.
func acceptSlotSnapshotReservedIncomingV2(ctx context.Context, incoming *slotSnapshotReservedIncomingV2) (*SlotSnapshotOwnerV1, error) {
	if incoming == nil {
		return nil, rewindFailureV1("the reserved Snapshot has no actual accepted native producer")
	}
	incoming.mu.Lock()
	defer incoming.mu.Unlock()
	// The SAME warm.owner.mu is held by the actual receiver.
	if err := incoming.checkOriginalV2(ctx); err != nil {
		return nil, err
	}
	if len(incoming.files) != 18 {
		return nil, rewindFailureV1("the original reserved Snapshot supplied another fixed native role vector")
	}
	source := incoming.warm.owner.source
	if source.Wall == nil {
		return nil, rewindFailureV1("the original reserved Snapshot has no protected complete VM Need")
	}
	need, err := vmneed.DecodeV1(source.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if err := need.ValidateWall(runtime.GOARCH, source.Wall.Level, source.Wall.Driver, source.Limits.MemoryMax); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	owner := &SlotSnapshotOwnerV1{reserved: incoming, warm: incoming.warm, operation: incoming.operation, line: incoming.line, invocation: incoming.invocation, holder: incoming.holder, state: incoming.state, sender: incoming.sender, need: need.Clone(), roles: incoming.files[:17], lease: incoming.files[17], lifetime: lifetime, cancel: cancel}
	owner.issuer = owner
	// Consume before validation can fail: every partial native right and the
	// real original lease remain reachable on this SAME returned owner.
	incoming.used, incoming.files = true, nil
	if err := owner.captureInvocationV1(); err != nil {
		return owner, err
	}
	if err := errors.Join(slotlaunch.CheckOriginalReservationLeaseV2(owner.lease), owner.checkRolesV1(ctx), owner.checkAcceptedLockedV1(ctx)); err != nil {
		return owner, err
	}
	owner.admitted = true
	return owner, nil
}
