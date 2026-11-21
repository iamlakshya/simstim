package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ForkDestinationV1 is the actual borrowed operation of a new empty child.
// Only the original native source controller may accept its original loan.
// Closing it releases this borrow only after its physical copy has joined.
type ForkDestinationV1 struct {
	mu        sync.Mutex
	owner     *OwnedSupervisorLease
	source    *SessionOperation
	operation *SessionOperation
	state     proto.NativeStopStateV1
	closed    bool
}

func forkFailureV1(code, message string) error {
	return UserError(code, message, "Retry fork through the current original sandbox and client connection.")
}

func (owner *OwnedSupervisorLease) AcceptForkDestinationV1(ctx context.Context, source *SessionOperation, line *StartupLineV1, frame proto.Frame, state proto.NativeStopStateV1) (_ *ForkDestinationV1, returned error) {
	if ctx == nil || source == nil || line == nil || owner == nil || owner.native == nil || !owner.Valid(state.Primary) || !owner.StartupCommittedV1() || source.native == nil || !source.native.borrowed || !source.Valid(state.Primary) || state.Generation != owner.generation {
		return nil, forkFailureV1("fork_source", "the fork destination has no original live source operation")
	}
	if err := errors.Join(owner.native.rootRef.CheckRoot(source.native.root), owner.CheckWarmSenderV1(line)); err != nil {
		return nil, err
	}
	var request proto.Request
	var args proto.NativeForkDestinationArgsV1
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != proto.NativeForkDestinationRequestV1 || request.Op != proto.NativeForkDestinationV1 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Scope.Version != 1 || args.Scope.Primary != state.Primary || args.Scope.Nonce != state.Nonce || !api.ValidSessionID(args.Child) || args.Child == state.Primary {
		return nil, errors.Join(forkFailureV1("fork_destination", "the fork destination loan differs from its original native scope"), line.RejectAncillary())
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
		return nil, forkFailureV1("fork_destination", "the child operation has no actual native sender")
	}
	if err := errors.Join(peer.CheckSender(sender), checkBorrowedFlockV1(file, sender)); err != nil {
		return nil, err
	}
	native, err := adoptStartupOperationFile(ctx, args.Child, file)
	if err != nil {
		return nil, err
	}
	adopted = true
	operation := sessionOperationFromNative(owner.Lifetime(), args.Child, native)
	result := &ForkDestinationV1{owner: owner, source: source, operation: operation, state: state}
	if err := result.check(ctx); err != nil {
		return nil, errors.Join(err, result.Close())
	}
	if err := emptyForkDestinationV1(operation.native.root); err != nil {
		return nil, errors.Join(err, result.Close())
	}
	if err := errors.Join(peer.CheckSender(sender), checkBorrowedFlockV1(file, sender)); err != nil {
		return nil, errors.Join(err, result.Close())
	}
	return result, nil
}

func emptyForkDestinationV1(root *os.Root) (returned error) {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	for {
		entries, err := file.ReadDir(16)
		for _, entry := range entries {
			if entry.Name() != lockFile {
				return forkFailureV1("fork_destination", "the destination is not its original unused child reservation")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (destination *ForkDestinationV1) check(ctx context.Context) error {
	if destination == nil || ctx == nil || destination.closed || destination.owner == nil || destination.source == nil || destination.operation == nil || !destination.owner.Valid(destination.state.Primary) || !destination.owner.StartupCommittedV1() || !destination.source.Valid(destination.state.Primary) || !destination.operation.Valid(destination.operation.Primary()) || destination.operation.Primary() == destination.state.Primary {
		return forkFailureV1("fork_destination", "the original child destination or source borrow ended")
	}
	return errors.Join(ctx.Err(), destination.owner.native.rootRef.CheckRoot(destination.source.native.root), destination.operation.native.check(ctx))
}

// Child is correlation data only. WithRootV1 checks the retained original
// source and child OP before and after borrowing the exact reservation root.
func (destination *ForkDestinationV1) Child() string {
	if destination == nil || destination.operation == nil {
		return ""
	}
	return destination.operation.Primary()
}

func (destination *ForkDestinationV1) WithRootV1(ctx context.Context, write func(*os.Root) error) error {
	if destination == nil || write == nil {
		return forkFailureV1("fork_destination", "the original child writer is absent")
	}
	destination.mu.Lock()
	defer destination.mu.Unlock()
	if err := destination.check(ctx); err != nil {
		return err
	}
	if err := write(destination.operation.native.root); err != nil {
		return err
	}
	return destination.check(ctx)
}

func (destination *ForkDestinationV1) Close() error {
	if destination == nil {
		return nil
	}
	destination.mu.Lock()
	defer destination.mu.Unlock()
	if destination.closed {
		return nil
	}
	if destination.operation == nil {
		return forkFailureV1("fork_destination", "the original child operation is absent")
	}
	if err := destination.operation.CloseWithError(); err != nil {
		return err
	}
	destination.closed = true
	return nil
}

// CheckForkSnapshotRootV1 only compares a root borrowed from the original
// engine with this SAME native source operation. It creates no storage owner.
func CheckForkSnapshotRootV1(ctx context.Context, source *SessionOperation, root *os.Root) error {
	if ctx == nil || source == nil || source.native == nil || !source.native.borrowed || !source.Valid(source.Primary()) || root == nil {
		return forkFailureV1("fork_source", "the snapshot root has no original borrowed source operation")
	}
	return errors.Join(ctx.Err(), source.native.rootRef.CheckRoot(root))
}
