package host

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Issued only after AcceptWarmOperationV1 adopted the actual current SCM OP
// and rechecked its original native sender. This original actor custody lets
// a joined worker continue while the physical reader handles another frame;
// it does not replace any current-frame check at request dispatch.
type warmIncomingOriginV1 struct {
	issuer    *warmIncomingOriginV1
	owner     *OwnedSupervisorLease
	operation *SessionOperation
	native    *sessionFileLease
	line      *StartupLineV1
	peer      *nativeStartupPeerV1
	sender    startupCredentials
	state     proto.NativeStopStateV1
}

func (owner *OwnedSupervisorLease) CheckWarmOperationV1(ctx context.Context, line *StartupLineV1, operation *SessionOperation) error {
	if ctx == nil || owner == nil || line == nil || operation == nil || operation.native == nil || !operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("the warm worker has no original incoming operation")
	}
	origin := operation.warmIncoming
	if origin == nil || origin.issuer != origin || origin.operation != operation || origin.native != operation.native || origin.owner != owner || origin.line != line || origin.peer == nil {
		return sessionLeaseError("the warm worker lost its actual accepted incoming producer")
	}
	owner.mu.Lock()
	valid := owner.native != nil && owner.Valid(operation.Primary()) && owner.startup != nil && owner.startup.ready && owner.generation == origin.state.Generation && owner.native.id == origin.state.Primary
	owner.mu.Unlock()
	line.mu.Lock()
	same := !line.closed && line.nativePeer == origin.peer
	line.mu.Unlock()
	if !valid || !same {
		return sessionLeaseError("the original warm worker owner or physical native line ended")
	}
	if err := errors.Join(ctx.Err(), owner.native.rootRef.CheckRoot(operation.native.root), operation.native.check(ctx), origin.peer.CheckSender(origin.sender)); err != nil {
		return err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	if current.Auxiliary != nil || current.Run.BootID != BootID() || current.Run.Generation != origin.state.Generation || startupDigest(current.VMNeedV1) != origin.state.Need || startupDigest(runtime) != origin.state.Runtime {
		return sessionLeaseError("the warm worker changed its original protected run")
	}
	return errors.Join(ctx.Err(), origin.peer.CheckSender(origin.sender), operation.native.check(ctx))
}
