//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// StoppedNoneSessionV1 retains only the actual original none creator's N5
// init/main/terminal completion. It supplies no whole-unit, VM, auxiliary,
// ordinary-holder or future cold-resume permission.
type StoppedNoneSessionV1 struct {
	self                       *StoppedNoneSessionV1
	mu                         sync.Mutex
	operation                  *SessionOperation
	warm                       *WarmResumeV1
	rejectedCold               *ColdResumeV1 // cleanup only, never an N5 authority
	rejectedDiagnostic         error
	completion                 *os.File
	operationFile              *os.File
	boot                       string
	holderIdentity             os.FileInfo
	committed, settled, closed bool
}

func StopNoneSessionForOperationV1(ctx context.Context, operation *SessionOperation) (result *StoppedNoneSessionV1, returned error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, sessionLeaseError("the unwalled stop has no original owning operation")
	}
	manifest, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !NoneSupervisorShapeV1(manifest) || !auxiliaryBootIDV1(BootID()) || manifest.Run.BootID != BootID() || manifest.Run.Generation == "" {
		return nil, sessionLeaseError("the N5 stop source is not an original unwalled supervisor")
	}
	source := &StoppedNoneSessionV1{operation: operation, boot: BootID()}
	source.self = source
	if err := source.bindCustodyV1(); err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			closeErr := source.Close()
			returned = errors.Join(returned, source.CloseDiagnosticsV4())
			if closeErr != nil {
				result = source
				returned = errors.Join(returned, closeErr)
			}
		}
	}()
	warm, cold, err := InspectResumeForOperationV1(ctx, operation)
	source.warm, source.rejectedCold = warm, cold
	if err != nil {
		return nil, err
	}
	if cold != nil {
		return nil, sessionLeaseError("the unwalled stop needs its original live creator")
	}
	if warm == nil || warm.owner == nil {
		return nil, sessionLeaseError("the original unwalled creator is unavailable")
	}
	if !slices.Contains(warm.owner.features, proto.FeatureNoneStopCompletionV1) {
		return nil, UserError("helper_old", "the original unwalled supervisor cannot produce native stop completion", "Use its original helper or restore it after a real host restart before stopping it.")
	}
	file, err := operation.native.duplicateLockedFile(ctx)
	if err != nil {
		return nil, err
	}
	source.operationFile = file
	owner := warm.owner
	owner.mu.Lock()
	args := proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: owner.state.Nonce}
	raw, err := json.Marshal(args)
	if err == nil {
		err = owner.line.SetDeadline(time.Time{})
	}
	if err == nil {
		err = owner.line.WriteFrame(proto.TRequest, proto.Request{ID: proto.NativeNonePrepareRequestV1, Op: proto.NativeNonePrepareV1, Args: raw})
	}
	if err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	var response proto.Response
	var state proto.NativeStopStateV1
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != proto.NativeNonePrepareRequestV1 || !response.OK || !api.ExactJSONFields(response.Result, state) || json.Unmarshal(response.Result, &state) != nil || state != owner.state {
		owner.mu.Unlock()
		_ = owner.line.RejectAncillary()
		return nil, sessionLeaseError("the original unwalled stop preparation changed its scope")
	}
	completion, sender, err := owner.line.takeLoanFile()
	if err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	if err := errors.Join(owner.invocation.peer.checkNativeSender(sender), owner.invocation.Check(ctx)); err != nil {
		completion.Close()
		owner.mu.Unlock()
		return nil, err
	}
	info, err := completion.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		completion.Close()
		owner.mu.Unlock()
		return nil, errors.Join(sessionLeaseError("the original unwalled completion role is not a native pipe"), err)
	}
	source.completion = completion
	source.holderIdentity, err = owner.holder.file.Stat()
	if err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	// After this actual send attempt, cancellation does not release the SAME
	// OP before the original physical creator's completion and process join.
	source.committed = true
	err = owner.line.WriteFrame(proto.TRequest, proto.Request{ID: proto.NativeNoneCommitRequestV1, Op: proto.NativeNoneCommitV1, Args: raw})
	owner.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var acknowledgement [2]byte
	count, readErr := io.ReadFull(completion, acknowledgement[:])
	if count != 1 || acknowledgement[0] != 1 || !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil, errors.Join(sessionLeaseError("the original unwalled init, main, terminal or OP did not confirm joined cleanup"), readErr)
	}
	// The pipe is not process exit. Independently join this SAME captured
	// supervisor pidfd; never substitute EOF, a saved PID or a deadline.
	for {
		polls := []unix.PollFd{{Fd: int32(owner.invocation.peer.pidfd), Events: unix.POLLIN}}
		count, err := unix.Poll(polls, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if count == 1 && polls[0].Revents&unix.POLLIN != 0 {
			break
		}
	}
	// Reap only when this caller is the original parent. P_PIDFD cannot name
	// a replacement process; ECHILD gives no reaping right and is ignored.
	var status unix.Siginfo
	if err := unix.Waitid(unix.P_PIDFD, owner.invocation.peer.pidfd, &status, unix.WEXITED|unix.WNOHANG, nil); err != nil && !errors.Is(err, unix.ECHILD) {
		return nil, err
	}
	source.settled = true
	if err := source.Check(context.Background(), operation); err != nil {
		return nil, err
	}
	complete = true
	return source, nil
}

// Only an actual storage consumer that deliberately removed the named source
// may use this narrower check. It remains tied to the SAME retained kernel
// objects and supplies no public path, cold source or replacement permission.
func (source *StoppedNoneSessionV1) checkNativeAfterRemovalV1(ctx context.Context, operation *SessionOperation) error {
	if source == nil || source.self != source || ctx == nil {
		return sessionLeaseError("the original N5 completion is absent")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed || source.rejectedCold != nil || !source.settled || !source.committed || source.operation != operation || operation == nil || !operation.active.Load() || source.warm == nil || source.warm.owner == nil || !auxiliaryBootIDV1(source.boot) || source.boot != BootID() {
		return sessionLeaseError("the original N5 completion source ended")
	}
	owner := source.warm.owner
	if owner.holder == nil || owner.holder.file == nil || owner.invocation == nil {
		return sessionLeaseError("the original N5 native objects are absent")
	}
	current, err := owner.holder.file.Stat()
	if err != nil || source.holderIdentity == nil || !os.SameFile(source.holderIdentity, current) {
		return errors.Join(sessionLeaseError("the retained original holder description changed"), err)
	}
	return errors.Join(ctx.Err(), CheckCaller(), owner.origin.checkIdentity(), ordinaryPeerExitedV1(owner.invocation.peer))
}

func (source *StoppedNoneSessionV1) Check(ctx context.Context, operation *SessionOperation) error {
	if source == nil || source.self != source || ctx == nil {
		return sessionLeaseError("the original N5 unwalled stop source is absent")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed || source.rejectedCold != nil || !source.committed || !source.settled || operation == nil || source.operation != operation || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || source.warm == nil || source.warm.owner == nil || source.operationFile == nil || source.completion == nil || !auxiliaryBootIDV1(source.boot) || source.boot != BootID() {
		return sessionLeaseError("the original N5 unwalled stop source ended or changed")
	}
	owner := source.warm.owner
	if owner.holder == nil || owner.invocation == nil || owner.origin == nil {
		return sessionLeaseError("the unwalled stop lost its original native roots")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.origin.checkIdentity(), owner.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.holder.root), ordinaryPeerExitedV1(owner.invocation.peer)); err != nil {
		return err
	}
	if err := owner.noneHolder.checkDescriptionV1(ctx, owner); err != nil {
		return err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !NoneSupervisorShapeV1(current) || current.Run.Generation != owner.state.Generation {
		return sessionLeaseError("the protected unwalled source changed after its original physical join")
	}
	return nil
}

func (source *StoppedNoneSessionV1) Close() error {
	if source == nil {
		return nil
	}
	if source.self != source {
		return sessionLeaseError("a copied N5 completion cannot release the original source")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return nil
	}
	if source.committed && !source.settled {
		if source.warm != nil && source.warm.owner != nil && source.warm.owner.line != nil {
			_ = source.warm.owner.line.Close()
		}
		return sessionLeaseError("the original unwalled stop and SAME operation remain retained because physical completion or loan closure is uncertain")
	}
	// A refused inspection may still return its actual partial Cold owner.
	// It remains cleanup-only and must settle before this stop releases OP.
	if source.rejectedCold != nil {
		if err := source.rejectedCold.Close(); err != nil {
			return err
		}
		source.rejectedDiagnostic = errors.Join(source.rejectedDiagnostic, source.rejectedCold.CloseDiagnosticsV4())
		source.rejectedCold = nil
	}
	var err error
	if source.warm != nil {
		if source.committed {
			// The server retains the actual incoming OP until its owned joins.
			// A failed/absent completion never yields a stopped source here.
			if source.warm.owner != nil {
				err = errors.Join(err, source.warm.owner.Close())
			}
		} else {
			err = errors.Join(err, source.warm.Close())
		}
	}
	if source.completion != nil {
		err = errors.Join(err, source.completion.Close())
	}
	if source.operationFile != nil {
		err = errors.Join(err, source.operationFile.Close())
	}
	if err == nil {
		source.closed = true
		source.releaseCustodyV1()
	}
	return err
}

// Report data from the SAME rejected cleanup owner grants no N5 or Cold role.
func (source *StoppedNoneSessionV1) CloseDiagnosticsV4() error {
	if source == nil || source.self != source {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	return errors.Join(source.rejectedDiagnostic, source.rejectedCold.CloseDiagnosticsV4())
}
