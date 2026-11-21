//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// The outside invocation keeps its SAME real factory, immutable actual Cmd,
// atomic pidfd, Wait and connection. Even an error returns this original owner
// so the caller can retain it. It never rebuilds session sources from a report.
type purgeOriginalInvocationV3 struct {
	mu                           sync.Mutex
	code                         *purgeAttemptCodeV2
	execution                    *purgeAttemptExecV1
	args                         api.PurgeArgs
	next                         uint64
	pendingBegin, broken, closed bool
	last                         api.PurgeResult
	completion                   *purgeOriginalCompletionV3
}

func newOriginalPurgeInvocationV3(ctx context.Context, args api.PurgeArgs) (*purgeOriginalInvocationV3, error) {
	if ctx == nil {
		return nil, placementError("the original removal invocation has no request lifetime")
	}
	placement, err := factsPlacementOwnerV1(ctx)
	if err != nil {
		return nil, err
	}
	owner := &purgeOriginalInvocationV3{args: args, next: 1}
	owner.code, err = newPurgeAttemptCodeV2(ctx, placement, args)
	if err != nil {
		return owner, err
	}
	owner.execution, err = startPurgeAttemptExecV1(ctx, owner.code, args)
	if err != nil {
		return owner, err
	}
	if err := owner.execution.admitOriginalCodeV3(ctx); err != nil {
		owner.broken = true
		return owner, err
	}
	owner.pendingBegin = true
	return owner, nil
}

func (owner *purgeOriginalInvocationV3) Call(ctx context.Context, operation string) (api.PurgeResult, error) {
	if owner == nil || ctx == nil {
		return api.PurgeResult{}, placementError("the original purge invocation is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed && operation == purgeCloseV3 && owner.completion != nil {
		return owner.last, owner.completion.Close(ctx)
	}
	if owner.closed || owner.execution == nil || owner.code == nil || operation != purgeContinueV3 && operation != purgeAbortV3 && operation != purgeCloseV3 {
		return owner.last, placementError("the original invocation has no same live closed continuation")
	}
	if operation == purgeCloseV3 && owner.completion != nil {
		return owner.last, owner.finishOriginalV3(ctx)
	}
	if owner.pendingBegin && operation != purgeContinueV3 {
		_ = owner.execution.cancelRequestV2()
		if owner.execution.line != nil {
			_ = owner.execution.line.conn.Close()
		}
		owner.pendingBegin, owner.broken = false, true
	}
	if owner.broken {
		return owner.relayOriginalV3(ctx, operation)
	}
	line := owner.execution.line
	if line == nil {
		return owner.last, placementError("the original invocation lost its actual native line")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = line.conn.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	id := owner.next
	if owner.pendingBegin {
		owner.pendingBegin = false
	} else {
		if id == ^uint64(0) {
			return owner.last, placementError("the original removal exhausted its request identifiers")
		}
		id++
		request, err := purgeRequestFrameV3(id, operation, owner.args)
		if err != nil {
			return owner.last, err
		}
		if err := line.WriteFrame(proto.TRequest, request); err != nil {
			owner.broken = true
			return owner.last, err
		}
	}
	owner.next = id
	result, err := readOriginalPurgeResponseV3(ctx, owner.execution, id)
	owner.last = result
	if err != nil {
		owner.broken = true
		_ = line.conn.Close()
		return result, err
	}
	if operation == purgeCloseV3 {
		if err := line.WriteFrame(proto.TResponse, proto.Response{ID: id, OK: true}); err != nil {
			owner.broken = true
			return result, err
		}
		if err := owner.finishOriginalV3(ctx); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (owner *purgeOriginalInvocationV3) relayOriginalV3(ctx context.Context, operation string) (api.PurgeResult, error) {
	if owner.next == ^uint64(0) {
		return owner.last, placementError("the original removal exhausted its request identifiers")
	}
	owner.next++
	if operation == purgeCloseV3 {
		select {
		case <-owner.execution.waitDone:
			if err := owner.finishOriginalV3(ctx); err != nil {
				return owner.last, err
			}
			return owner.last, nil
		default:
		}
	}
	if err := owner.execution.checkRetainedCreationV3(ctx); err != nil {
		return owner.last, err
	}
	image, err := captureOwnPurgeRelayImageV3()
	if err != nil {
		return owner.last, err
	}
	defer image.Close()
	if !os.SameFile(image.mapped, owner.code.sourceInfo) {
		return owner.last, placementError("the retained original invocation changed its own current kernel code")
	}
	request, err := purgeRequestFrameV3(owner.next, operation, owner.args)
	if err != nil {
		return owner.last, err
	}
	result, err := relayOriginalPurgeRequestV3(ctx, image, request, purgeRequestDataV3{Version: 3, Args: owner.args}, owner.execution)
	if err != nil {
		return result, err
	}
	owner.last = result
	if operation == purgeCloseV3 {
		if err := owner.finishOriginalV3(ctx); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (owner *purgeOriginalInvocationV3) finishOriginalV3(ctx context.Context) error {
	if owner.completion == nil {
		select {
		case <-owner.execution.waitDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		completion, err := newPurgeOriginalCompletionV3(owner)
		if err != nil {
			return err
		}
		owner.completion = completion
	}
	if err := owner.completion.Close(ctx); err != nil {
		return err
	}
	owner.closed = true
	return nil
}

// Close cancels an existing request. Native exit is mandatory before code
// loans release. It never kills a process which may hold private subjects.
func (owner *purgeOriginalInvocationV3) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	if owner.execution != nil {
		if err := owner.execution.Close(); err != nil {
			return err
		}
	}
	if owner.code != nil {
		if err := owner.code.Close(); err != nil {
			return err
		}
	}
	owner.closed = true
	return nil
}

// Continuation observes the SAME actual creation even after its own code
// names are gone. This issues no execution, private loan, OP or stopped source.
func (owner *purgeAttemptExecV1) checkRetainedCreationV3(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return placementError("the retained original creation is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || !owner.admissionSent || owner.start == nil || owner.scope == nil || owner.command == nil || owner.command.Process != owner.process || owner.process == nil || owner.peer == nil || owner.peer.pid != owner.process.Pid || owner.peer.identity == nil || owner.creation == nil || owner.creationFD < 0 || int(owner.creation.Fd()) != owner.creationFD || owner.code == nil || owner.loan == nil || owner.loan.creator != owner.code {
		return placementError("the continuation has no actual originally admitted fixed creation")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.start.checkRetainedSpecV3(ctx), owner.scope.checkLiveV3(ctx), owner.peer.checkLive(), unix.PidfdSendSignal(int(owner.creation.Fd()), 0, nil, 0)); err != nil {
		return err
	}
	owner.code.mu.Lock()
	bound := owner.code.execution == owner && !owner.code.closed
	owner.code.mu.Unlock()
	if !bound {
		return placementError("the original continuation lost its actual code-factory creation binding")
	}
	info, err := owner.loan.CopiedCode.Stat()
	if err != nil || !os.SameFile(info, owner.code.copyInfo) || !os.SameFile(info, owner.peer.identity.expected) {
		return errors.Join(placementError("the retained creation changed its original native copied code object"), err)
	}
	if err := verifyOriginalPurgeCodeBytesV2(ctx, owner.code.source, owner.loan.CopiedCode, owner.code.sourceInfo); err != nil {
		return err
	}
	return errors.Join(checkCreatedHelperArgumentsV1(owner.peer, owner.argv), owner.peer.checkLive())
}
