//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"golang.org/x/sys/unix"
)

// SlotSnapshotOwnerV1 is issued only by the accepted original Warm lane.
// It retains that actual issuer and native objects. A descriptor vector or
// protected manifest cannot construct it. Legacy custody grants live/read
// operations only; it has no reservation for a stopped slot account.
type SlotSnapshotOwnerV1 struct {
	mu                                    sync.Mutex
	operations                            sync.Mutex
	active                                sync.WaitGroup
	issuer                                *SlotSnapshotOwnerV1
	incoming                              *slotSnapshotIncomingV1
	reserved                              *slotSnapshotReservedIncomingV2
	warm                                  *WarmResumeV1
	operation                             *SessionOperation
	line                                  *StartupLineV1
	invocation                            *nativeStopInvocationV1
	holder                                *sessionFileLease
	state                                 proto.NativeStopStateV1
	sender                                startupCredentials
	lease                                 *os.File
	need                                  vmneed.NeedV1
	roles                                 []*os.File
	supervisor, group, code, user, cgroup *os.File
	lifetime                              context.Context
	cancel                                context.CancelFunc
	pending                               []*slotlaunch.CheckpointOperationOwnerV1
	incomplete                            []*slotSnapshotReadV1
	backend                               *slotSnapshotStoppedV2
	failedFiles                           []*os.File
	cold                                  *ColdResumeV1
	takeover                              *slotColdTakeoverV2
	failedArtifacts                       []*auxiliaryArtifactsV1
	slotRetry                             *slotRetryOwnerV2
	roleInfo                              []os.FileInfo
	admitted, closing, closed             bool
	closeErr                              error
}

func acceptSlotSnapshotIncomingV1(ctx context.Context, incoming *slotSnapshotIncomingV1) (*SlotSnapshotOwnerV1, error) {
	if incoming == nil {
		return nil, rewindFailureV1("the Snapshot has no actual accepted native producer")
	}
	incoming.mu.Lock()
	defer incoming.mu.Unlock()
	// The caller holds warm.owner.mu. Do not recursively call warm.Check.
	if err := incoming.checkOriginalV1(ctx); err != nil {
		return nil, err
	}
	if len(incoming.files) != 17 {
		return nil, rewindFailureV1("the original Snapshot supplied another fixed native role vector")
	}
	source := incoming.warm.owner.source
	if source.Wall == nil {
		return nil, rewindFailureV1("the original Snapshot has no protected VM Need")
	}
	need, err := vmneed.DecodeV1(source.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if err := need.ValidateWall(runtime.GOARCH, source.Wall.Level, source.Wall.Driver, source.Limits.MemoryMax); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	owner := &SlotSnapshotOwnerV1{incoming: incoming, warm: incoming.warm, operation: incoming.operation, line: incoming.line, invocation: incoming.invocation, holder: incoming.holder, state: incoming.state, sender: incoming.sender, need: need.Clone(), roles: incoming.files, lifetime: lifetime, cancel: cancel}
	owner.issuer = owner
	// Publish custody before any subsequent pin/check can fail. The caller
	// receives this SAME actual owner on every later error.
	incoming.used, incoming.files = true, nil
	if err := owner.captureInvocationV1(); err != nil {
		return owner, err
	}
	if err := errors.Join(owner.checkRolesV1(ctx), owner.checkAcceptedLockedV1(ctx)); err != nil {
		return owner, err
	}
	owner.admitted = true
	return owner, nil
}

func (owner *SlotSnapshotOwnerV1) captureInvocationV1() error {
	invocation := owner.invocation
	peer := invocation.peer
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.closed || peer.identity == nil || peer.pidfd < 0 {
		return rewindFailureV1("the original Snapshot invocation lost its native code and namespace identity")
	}
	identity := peer.identity
	identity.mu.RLock()
	defer identity.mu.RUnlock()
	if identity.closed {
		return rewindFailureV1("the original Snapshot helper identity ended")
	}
	fd, err := unix.FcntlInt(uintptr(peer.pidfd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return err
	}
	owner.supervisor = os.NewFile(uintptr(fd), "original-snapshot-supervisor")
	for _, role := range []struct {
		input  *os.File
		output **os.File
	}{{invocation.cgroup, &owner.group}, {identity.executable, &owner.code}, {identity.user, &owner.user}, {identity.cgroup, &owner.cgroup}} {
		duplicate, err := duplicateSlotSnapshotFileV1(role.input)
		if duplicate != nil {
			*role.output = duplicate
		}
		if err != nil {
			return err
		}
	}
	actual, err := identity.executable.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(actual, invocation.executable) {
		return rewindFailureV1("the actual original Snapshot supervisor code changed")
	}
	return nil
}

// checkAcceptedLockedV1 requires the actual Warm owner mutex to be held.
func (owner *SlotSnapshotOwnerV1) checkAcceptedLockedV1(ctx context.Context) error {
	if err := owner.checkIssuerV1(); err != nil {
		return err
	}
	captured := owner.warm.owner
	if captured.operation != owner.operation || captured.line != owner.line || captured.invocation != owner.invocation || captured.holder != owner.holder || captured.state != owner.state || captured.origin == nil || captured.closed || captured.stopSent || captured.settled || !owner.warm.loanSent || owner.operation.native == nil || owner.operation.native.borrowed || !owner.operation.Valid(owner.operation.Primary()) {
		return rewindFailureV1("the original Snapshot changed its accepted Warm source or owning operation")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), captured.origin.checkIdentity(), owner.invocation.Check(ctx), owner.invocation.peer.checkNativeSender(owner.sender), owner.holder.check(ctx), owner.operation.native.check(ctx), owner.operation.native.rootRef.CheckRoot(owner.holder.root)); err != nil {
		return err
	}
	current, err := owner.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	if current.Run.Generation != owner.state.Generation || current.Run.BootID != BootID() || startupDigest(current.VMNeedV1) != owner.state.Need || startupDigest(runtime) != owner.state.Runtime {
		return rewindFailureV1("the original protected Snapshot Need or runtime changed")
	}
	return nil
}

func (owner *SlotSnapshotOwnerV1) checkIssuerV1() error {
	if owner == nil || owner.issuer != owner || owner.warm == nil || owner.warm.owner == nil || owner.operation == nil || owner.line == nil || owner.invocation == nil || owner.holder == nil {
		return rewindFailureV1("the original outside Snapshot issuer ended")
	}
	if owner.incoming != nil && owner.reserved == nil {
		actual := owner.incoming
		if !actual.used || actual.warm != owner.warm || actual.operation != owner.operation || actual.line != owner.line || actual.invocation != owner.invocation || actual.holder != owner.holder || actual.state != owner.state || actual.sender != owner.sender || actual.request.ID != proto.NativeCheckpointCustodyRequestV1 || actual.request.Op != proto.NativeCheckpointCustodyV1 {
			return rewindFailureV1("the original live Snapshot issuer was substituted")
		}
		return nil
	}
	if owner.reserved != nil && owner.incoming == nil {
		actual := owner.reserved
		if !actual.used || actual.warm != owner.warm || actual.operation != owner.operation || actual.line != owner.line || actual.invocation != owner.invocation || actual.holder != owner.holder || actual.state != owner.state || actual.sender != owner.sender || actual.request.ID != proto.NativeCheckpointReservationRequestV2 || actual.request.Op != proto.NativeCheckpointReservationV2 || owner.lease == nil {
			return rewindFailureV1("the original reserved Snapshot issuer was substituted")
		}
		return slotlaunch.CheckOriginalReservationLeaseV2(owner.lease)
	}
	return rewindFailureV1("the Snapshot has no single original native issuer class")
}

func (owner *SlotSnapshotOwnerV1) beginV1(ctx context.Context) (func(), error) {
	if owner == nil || ctx == nil {
		return nil, rewindFailureV1("the Snapshot has no actual outside owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.issuer != owner || owner.warm == nil || !owner.admitted || owner.closing || owner.closed || owner.lifetime == nil || owner.lifetime.Err() != nil || ctx.Err() != nil {
		return nil, rewindFailureV1("the original outside Snapshot owner ended")
	}
	owner.active.Add(1)
	return owner.active.Done, nil
}

func (owner *SlotSnapshotOwnerV1) checkRunningV1(ctx context.Context) error {
	captured := owner.warm.owner
	captured.mu.Lock()
	defer captured.mu.Unlock()
	return errors.Join(owner.checkAcceptedLockedV1(ctx), owner.checkRolesV1(ctx))
}

// OpenStoppedBackendV1 requires the SAME genuine settled Warm source and
// continuously retained original keeper-issued allocation lease.
func (owner *SlotSnapshotOwnerV1) OpenStoppedBackendV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1) (checkpoint.BackendV1, func() error, error) {
	done, err := owner.beginV1(ctx)
	if err != nil {
		return nil, nil, err
	}
	keep := false
	defer func() {
		if !keep {
			done()
		}
	}()
	owner.operations.Lock()
	defer owner.operations.Unlock()
	owner.mu.Lock()
	closing := owner.closing || owner.closed
	owner.mu.Unlock()
	if closing {
		return nil, nil, rewindFailureV1("the original Snapshot closed before its stopped borrow")
	}
	if owner.reserved == nil || owner.incoming != nil || owner.lease == nil {
		return nil, nil, rewindFailureV1("this legacy Snapshot has no original keeper-issued allocation reservation")
	}
	if owner.backend != nil {
		return nil, nil, rewindFailureV1("the actual stopped Snapshot backend must close before another borrow")
	}
	if err := owner.checkStoppedV2(ctx, operation, stopped); err != nil {
		return nil, nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	backend := &slotSnapshotStoppedV2{owner: owner, operation: operation, stopped: stopped, done: done, lifetime: lifetime, cancel: cancel}
	backend.issuer = backend
	owner.backend = backend
	keep = true
	return backend, backend.Close, nil
}

func (owner *SlotSnapshotOwnerV1) checkStoppedV2(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1) error {
	if err := owner.checkIssuerV1(); err != nil {
		return err
	}
	if owner.reserved == nil || owner.incoming != nil || owner.lease == nil || stopped == nil || operation != owner.operation || stopped != owner.warm.owner {
		return rewindFailureV1("the stopped Snapshot has another actual Warm source, operation or allocation lease")
	}
	return errors.Join(stopped.Check(ctx, operation), owner.checkPhysicalExitV1())
}

// Close fences new work and joins actual root operations before closing this
// owner's handles. Selected read owners have their own actual duplicates.
func (owner *SlotSnapshotOwnerV1) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	if owner.issuer != owner {
		owner.mu.Unlock()
		return rewindFailureV1("a copied Snapshot cannot release the original owner")
	}
	if owner.closed {
		err := owner.closeErr
		owner.mu.Unlock()
		return err
	}
	owner.closing = true
	owner.mu.Unlock()
	// The backend pointer and its preparation are published only under this
	// mutex. A previously begun OpenStopped rechecks closing before publish.
	owner.operations.Lock()
	backend := owner.backend
	owner.operations.Unlock()
	if backend != nil {
		if err := backend.Close(); err != nil {
			return err
		}
	}
	owner.cancel()
	owner.active.Wait()
	owner.operations.Lock()
	defer owner.operations.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return owner.closeErr
	}
	if retry := owner.slotRetry; retry != nil {
		if retry.operation != nil {
			owner.closeErr = errors.Join(owner.closeErr, retry.operation.Close())
		}
		if !retry.published && retry.next != nil && retry.next.artifacts != nil {
			owner.closeErr = errors.Join(owner.closeErr, retry.next.artifacts.Close())
		}
	}
	for _, artifacts := range owner.failedArtifacts {
		owner.closeErr = errors.Join(owner.closeErr, artifacts.Close())
	}
	for _, selected := range owner.incomplete {
		owner.closeErr = errors.Join(owner.closeErr, selected.Close())
	}
	for _, operation := range owner.pending {
		owner.closeErr = errors.Join(owner.closeErr, operation.Close())
	}
	if owner.closeErr != nil {
		return owner.closeErr
	}
	for _, file := range append(append([]*os.File(nil), owner.roles...), append(append([]*os.File(nil), owner.failedFiles...), owner.supervisor, owner.group, owner.code, owner.user, owner.cgroup, owner.lease)...) {
		if file != nil {
			owner.closeErr = errors.Join(owner.closeErr, file.Close())
		}
	}
	owner.closed = owner.closeErr == nil
	return owner.closeErr
}
