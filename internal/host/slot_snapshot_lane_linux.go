//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// slotSnapshotIncomingV1 exists only on the same actual accepted Warm lane.
// It retains the genuine live capture, not an unready Stopped permission.
// H15's closed outside factory consumes this concrete incoming producer.
type slotSnapshotIncomingV1 struct {
	mu         sync.Mutex
	warm       *WarmResumeV1
	operation  *SessionOperation
	line       *StartupLineV1
	invocation *nativeStopInvocationV1
	holder     *sessionFileLease
	request    proto.Request
	response   proto.Frame
	state      proto.NativeStopStateV1
	sender     startupCredentials
	files      []*os.File
	used       bool
}

func (incoming *slotSnapshotIncomingV1) checkOriginalV1(ctx context.Context) error {
	if incoming == nil || ctx == nil || incoming.warm == nil || incoming.warm.owner == nil || incoming.operation == nil || incoming.line == nil || incoming.invocation == nil || incoming.holder == nil || incoming.used {
		return rewindFailureV1("the actual incoming Snapshot producer ended or was consumed")
	}
	owner := incoming.warm.owner
	if owner.operation != incoming.operation || owner.line != incoming.line || owner.invocation != incoming.invocation || owner.holder != incoming.holder || owner.state != incoming.state || owner.origin == nil || incoming.invocation.peer == nil || owner.closed || owner.stopSent || owner.settled || !incoming.warm.loanSent || incoming.operation.native == nil || incoming.operation.native.borrowed || !incoming.operation.Valid(incoming.operation.Primary()) || incoming.request.ID != proto.NativeCheckpointCustodyRequestV1 || incoming.request.Op != proto.NativeCheckpointCustodyV1 {
		return rewindFailureV1("the incoming Snapshot changed its same accepted source, operation or request")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.origin.checkIdentity(), incoming.invocation.Check(ctx), incoming.invocation.peer.checkNativeSender(incoming.sender), incoming.holder.check(ctx), incoming.operation.native.check(ctx), incoming.operation.native.rootRef.CheckRoot(incoming.holder.root)); err != nil {
		return err
	}
	current, err := incoming.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	if current.Run.Generation != incoming.state.Generation || current.Run.BootID != BootID() || startupDigest(current.VMNeedV1) != incoming.state.Need || startupDigest(runtime) != incoming.state.Runtime {
		return rewindFailureV1("the original protected Snapshot source changed during its physical loan")
	}
	return nil
}

func (incoming *slotSnapshotIncomingV1) closeUnconsumedV1() (failure error) {
	if incoming == nil {
		return nil
	}
	incoming.mu.Lock()
	defer incoming.mu.Unlock()
	if incoming.used {
		return nil
	}
	for _, file := range incoming.files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	incoming.files = nil
	return failure
}

func (warm *WarmResumeV1) captureSlotSnapshotV1(ctx context.Context, operation *SessionOperation) (_ *SlotSnapshotOwnerV1, local bool, returned error) {
	if err := warm.Check(ctx, operation); err != nil {
		return nil, false, err
	}
	owner := warm.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !slices.Contains(owner.features, proto.FeatureNativeCheckpointCustodyV1) {
		return nil, false, UserError("helper_old", "the running supervisor cannot transfer its original checkpoint owner", "Update the helper and use a sandbox started by the current version.")
	}
	if err := owner.line.armSlotSnapshotV1(); err != nil {
		return nil, false, err
	}
	defer func() { returned = errors.Join(returned, owner.line.finishSlotSnapshotV1()) }()
	args, err := json.Marshal(proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: owner.state.Nonce})
	if err != nil {
		return nil, false, err
	}
	request := proto.Request{ID: proto.NativeCheckpointCustodyRequestV1, Op: proto.NativeCheckpointCustodyV1, Args: args}
	if err := owner.line.WriteFrame(proto.TRequest, request); err != nil {
		return nil, false, err
	}
	cancelled := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { cancelled <- warm.writeWarmControlV1(5, proto.NativeWarmCancelV1) })
	defer func() {
		if !stop() {
			returned = errors.Join(returned, <-cancelled)
		}
	}()
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return nil, false, err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != request.ID {
		return nil, false, errors.Join(rewindFailureV1("the Snapshot transfer returned another original request"), owner.line.RejectAncillary())
	}
	if !response.OK {
		if response.Error != nil {
			return nil, false, errors.Join(response.Error, owner.line.RejectAncillary())
		}
		return nil, false, errors.Join(rewindFailureV1("the original Snapshot transfer was refused"), owner.line.RejectAncillary())
	}
	var result proto.NativeCheckpointCustodyResultV1
	if !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, &result) != nil || result.State != owner.state {
		return nil, false, errors.Join(rewindFailureV1("the Snapshot transfer changed its actual native scope"), owner.line.RejectAncillary())
	}
	if result.Mode == "local" {
		if err := errors.Join(owner.line.RejectAncillary(), owner.checkCurrentStopSenderV1(ctx), ctx.Err()); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	if result.Mode != "slot" {
		return nil, false, errors.Join(rewindFailureV1("the Snapshot transfer has an unknown original storage mode"), owner.line.RejectAncillary())
	}
	files, sender, err := owner.line.takeSlotSnapshotV1()
	if err != nil {
		return nil, false, err
	}
	incoming := &slotSnapshotIncomingV1{warm: warm, operation: operation, line: owner.line, invocation: owner.invocation, holder: owner.holder, request: request, response: frame, state: owner.state, sender: sender, files: files}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, incoming.closeUnconsumedV1())
		}
	}()
	if err := incoming.checkOriginalV1(ctx); err != nil {
		return nil, false, err
	}
	accepted, err := acceptSlotSnapshotIncomingV1(ctx, incoming)
	return accepted, false, err
}
