package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// AcceptWarmOperationV1 borrows only the original incoming OP from this
// already identified native line into the actual committed holder. The
// kernel OFD remains held until that request's worker and cleanup have joined.
func (owner *OwnedSupervisorLease) AcceptWarmOperationV1(ctx context.Context, line *StartupLineV1, frame proto.Frame, state proto.NativeStopStateV1) (_ *SessionOperation, returned error) {
	if owner == nil || ctx == nil || line == nil {
		return nil, sessionLeaseError("the warm loan has no actual native holder or line")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.native == nil || !owner.Valid(owner.native.id) || owner.startup == nil || !owner.startup.ready || state.Primary != owner.native.id || state.Generation != owner.generation {
		return nil, sessionLeaseError("the original warm native owner ended or changed")
	}
	var request proto.Request
	var args proto.NativeStopArgsV1
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != 8 || request.Op != proto.NativeWarmLoanV1 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Primary != state.Primary || args.Nonce != state.Nonce {
		return nil, errors.Join(sessionLeaseError("the warm operation loan has another original scope"), line.RejectAncillary())
	}
	file, sender, err := line.takeLoanFile()
	if err != nil {
		return nil, err
	}
	adopted := false
	defer func() {
		if !adopted {
			returned = errors.Join(returned, file.Close())
		}
	}()
	line.mu.Lock()
	peer := line.nativePeer
	line.mu.Unlock()
	if peer == nil {
		return nil, sessionLeaseError("the warm incoming operation has no original native sender")
	}
	if err := errors.Join(peer.CheckSender(sender), checkBorrowedFlockV1(file, sender)); err != nil {
		return nil, err
	}
	native, err := adoptStartupOperationFile(ctx, state.Primary, file)
	if err != nil {
		return nil, err
	}
	adopted = true
	operation := sessionOperationFromNative(owner.Lifetime(), state.Primary, native)
	fail := func(err error) (*SessionOperation, error) { return nil, errors.Join(err, operation.CloseWithError()) }
	if err := owner.native.rootRef.CheckRoot(native.root); err != nil {
		return fail(err)
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return fail(err)
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return fail(err)
	}
	if current.Wall == nil || current.Auxiliary != nil || current.Run.Generation != state.Generation || current.Run.BootID != BootID() || startupDigest(current.VMNeedV1) != state.Need || startupDigest(runtime) != state.Runtime {
		return fail(sessionLeaseError("the original protected warm source changed before its native loan"))
	}
	if err := errors.Join(peer.CheckSender(sender), checkBorrowedFlockV1(file, sender), native.check(ctx)); err != nil {
		return fail(err)
	}
	return operation, nil
}

// CheckWarmSenderV1 checks the current frame against the original accepted
// native connector. It lends no descriptor and creates no alternate scope.
func (owner *OwnedSupervisorLease) CheckWarmSenderV1(line *StartupLineV1) error {
	if owner == nil || line == nil {
		return sessionLeaseError("the warm control has no actual native source")
	}
	owner.mu.Lock()
	valid := owner.native != nil && owner.Valid(owner.native.id) && owner.startup != nil && owner.startup.ready
	owner.mu.Unlock()
	if !valid {
		return sessionLeaseError("the original warm holder ended")
	}
	line.mu.Lock()
	peer := line.nativePeer
	line.mu.Unlock()
	if peer == nil {
		return sessionLeaseError("the warm control has no original native connector")
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	return peer.CheckSender(sender)
}
