package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ForkDestinationV1 is the actual borrowed operation of a new empty child.
// Only the original native source controller may accept its original loan.
// Closing it releases this borrow only after its physical copy has joined.
type ForkDestinationV1 struct {
	issuer             *ForkDestinationV1
	pendingFile, probe *os.File
	pendingRoot        *os.Root
	retirement         nativeDescriptorRetirementsV1
	accepted           bool
	closeErr           error
	mu                 sync.Mutex
	owner              *OwnedSupervisorLease
	source             *SessionOperation
	operation          *SessionOperation
	state              proto.NativeStopStateV1
	closed             bool
}

func forkFailureV1(code, message string) error {
	return UserError(code, message, "Retry fork through the current original sandbox and client connection.")
}

func (owner *OwnedSupervisorLease) AcceptForkDestinationV1(ctx context.Context, source *SessionOperation, line *StartupLineV1, frame proto.Frame, state proto.NativeStopStateV1) (*ForkDestinationV1, error) {
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
	// Retain the actual materialised loan before any later identity or root
	// check can fail. This partial is cleanup-only until every original
	// acceptance check below succeeds; files alone never issue a destination.
	result := &ForkDestinationV1{owner: owner, source: source, state: state, pendingFile: file}
	result.issuer = result
	line.mu.Lock()
	peer := line.nativePeer
	line.mu.Unlock()
	if peer == nil {
		return result, forkFailureV1("fork_destination", "the child operation has no actual native sender")
	}
	if err := errors.Join(peer.CheckSender(sender), checkBorrowedFlockV1(file, sender)); err != nil {
		return result, err
	}
	// This is the same closed fork issuer and exact OP role, with resource
	// custody retained before each fallible step. The generic startup adopter
	// remains unchanged; no generic Source-from-FD constructor is added.
	result.pendingRoot, err = cachefs.OpenPrivateRoot(paths.SessionDir(args.Child), false)
	if err != nil {
		return result, err
	}
	rootRef, err := cachefs.CaptureRoot(result.pendingRoot)
	if err != nil {
		return result, err
	}
	fileRef, err := cachefs.CaptureFile(result.pendingRoot, lockFile, file, 0600)
	if err != nil {
		return result, err
	}
	native := &sessionFileLease{root: result.pendingRoot, file: file, rootRef: rootRef, fileRef: fileRef, id: args.Child, path: paths.SessionDir(args.Child), name: lockFile, borrowed: true}
	result.operation = sessionOperationFromNative(owner.Lifetime(), args.Child, native)
	result.pendingRoot, result.pendingFile = nil, nil // SAME objects now belong to the real OP.
	if err := result.checkOriginalV1(ctx); err != nil {
		return result, err
	}
	if err := result.emptyV1(); err != nil {
		return result, err
	}
	if err := errors.Join(peer.CheckSender(sender), checkBorrowedFlockV1(file, sender), result.checkOriginalV1(ctx)); err != nil {
		return result, err
	}
	result.accepted = true
	return result, nil
}

func (destination *ForkDestinationV1) emptyV1() (returned error) {
	file, err := destination.operation.native.root.Open(".")
	if err != nil {
		return err
	}
	destination.probe = file
	defer func() { returned = errors.Join(returned, destination.retirement.closeFilesV4([]*os.File{file})) }()
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
	if destination == nil || !destination.accepted {
		return forkFailureV1("fork_destination", "the actual child destination has not completed its original acceptance")
	}
	return destination.checkOriginalV1(ctx)
}

func (destination *ForkDestinationV1) checkOriginalV1(ctx context.Context) error {
	if destination == nil || destination.issuer != destination || ctx == nil || destination.closed || destination.owner == nil || destination.source == nil || destination.operation == nil || !destination.owner.Valid(destination.state.Primary) || !destination.owner.StartupCommittedV1() || !destination.source.Valid(destination.state.Primary) || !destination.operation.Valid(destination.operation.Primary()) || destination.operation.Primary() == destination.state.Primary {
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
	if destination.issuer != destination {
		return forkFailureV1("fork_destination", "the original child destination has no actual received resource owner")
	}
	if destination.closeErr != nil || destination.closed {
		return destination.closeErr
	}
	// Every partial remains reachable, including a root opened before its
	// receipt check and a failed emptiness-reader close. No LOCK_UN is used.
	if destination.operation != nil {
		destination.closeErr = errors.Join(destination.closeErr, destination.operation.CloseWithError())
	}
	destination.closeErr = errors.Join(destination.closeErr, destination.retirement.closeFilesV4([]*os.File{destination.pendingFile, destination.probe}), destination.retirement.closeRootsV4([]*os.Root{destination.pendingRoot}))
	if destination.closeErr == nil {
		destination.closed = true
	}
	return destination.closeErr
}

// CheckForkSnapshotRootV1 only compares a root borrowed from the original
// engine with this SAME native source operation. It creates no storage owner.
func CheckForkSnapshotRootV1(ctx context.Context, source *SessionOperation, root *os.Root) error {
	if ctx == nil || source == nil || source.native == nil || !source.native.borrowed || !source.Valid(source.Primary()) || root == nil {
		return forkFailureV1("fork_source", "the snapshot root has no original borrowed source operation")
	}
	return errors.Join(ctx.Err(), source.native.rootRef.CheckRoot(root))
}
