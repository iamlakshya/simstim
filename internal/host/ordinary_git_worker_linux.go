//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"golang.org/x/sys/unix"
)

// This result retains the actual original helper creation, control reader and
// root loans. Counts alone cannot create a completed or current inspection.
type joinedOrdinaryGitInspectionV2 struct {
	mu          sync.Mutex
	operation   *SessionOperation
	preparation *OrdinaryPurgePreparationV2
	permit      *ordinaryActiveGitInspectionPermitV2
	roots       *ordinaryActiveGitRootsV2
	endpoint    *purgePrivateEndpointV2
	command     *exec.Cmd
	start       *ordinaryGitStartV2
	creation    *os.File
	creationFD  int
	code        *os.File
	purgeImage  *purgeAttemptImageV2
	parent      *os.File
	peer        *nativeStartupPeerV1
	line        *StartupLineV1
	waitDone    chan struct{}
	waitErr     error
	counts      purgeGitInspectionV1
	dispatched  bool
	complete    bool
	closed      bool
}

func inspectOrdinaryActiveGitV2(ctx context.Context, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2, permit *ordinaryActiveGitInspectionPermitV2) (result *joinedOrdinaryGitInspectionV2, returned error) {
	if ctx == nil || permit == nil || preparation == nil || preparation.owner == nil {
		return nil, sessionLeaseError("the active Git worker has no original inspection owners")
	}
	if err := permit.Check(ctx, operation, preparation); err != nil {
		return nil, err
	}
	if preparation.owner.managed != nil {
		return nil, sessionLeaseError("managed fixed Git inspection needs its distinct original unit creation and settlement producer")
	}
	owner := &joinedOrdinaryGitInspectionV2{operation: operation, preparation: preparation, permit: permit, creationFD: -1}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	var err error
	owner.roots, err = permit.duplicateRootsV2(ctx, operation, preparation)
	if err != nil {
		return nil, err
	}
	owner.endpoint, err = newPurgePrivateEndpointV2()
	if err != nil {
		return nil, err
	}
	owner.purgeImage, err = ordinaryPurgeImageForPlacementV3(ctx, preparation.owner.origin)
	if err != nil {
		return nil, err
	}
	if owner.purgeImage != nil {
		owner.code, err = duplicateOrdinaryPurgeCodeV3(ctx, owner.purgeImage)
	} else {
		owner.code, err = os.Open("/proc/self/exe")
	}
	if err != nil {
		return nil, err
	}
	codeInfo, err := owner.code.Stat()
	if err != nil || !os.SameFile(codeInfo, preparation.owner.origin.executableRef) {
		return nil, errors.Join(sessionLeaseError("the fixed worker has another actual current helper image"), err)
	}
	owner.start, err = newOrdinaryGitStartV2(owner)
	if err != nil {
		return nil, err
	}
	owner.command = owner.start.command
	if err := owner.start.startActualV2(ctx); err != nil {
		return nil, err
	}
	if owner.creation == nil || owner.creationFD < 0 {
		return nil, sessionLeaseError("the fixed helper has no atomic original creation descriptor")
	}
	peerFD, err := unix.FcntlInt(owner.creation.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	process, err := os.OpenRoot("/proc/" + strconv.Itoa(owner.command.Process.Pid))
	if err != nil {
		_ = unix.Close(peerFD)
		return nil, err
	}
	owner.peer = &nativeStartupPeerV1{pid: owner.command.Process.Pid, uid: uint32(os.Getuid()), pidfd: peerFD, proc: process}
	workContext, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stopLifetime := context.AfterFunc(permit.lifetime, cancel)
	defer stopLifetime()
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(workContext, func() {
		defer close(callbackDone)
		_ = owner.endpoint.listener.Close()
		owner.mu.Lock()
		if owner.line != nil {
			_ = owner.line.conn.Close()
		}
		owner.mu.Unlock()
	})
	defer func() {
		if !stop() {
			<-callbackDone
		}
	}()
	if err := owner.acceptOriginalWorkerV2(workContext); err != nil {
		return nil, err
	}
	if err := permit.Check(workContext, operation, preparation); err != nil {
		return nil, err
	}
	plan := ordinaryGitWorkerPlanV2{Version: 2, Nonce: preparation.owner.state.Nonce, Branch: permit.origin.Branch}
	arguments, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	request := proto.Request{ID: 1, Op: gitOpPurgeInspectV1, Args: arguments}
	if err := owner.line.WriteFrame(proto.TRequest, request); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	// Private read loans are sent only after binding the current native sender
	// and code to this exact original atomic child creation.
	owner.dispatched = true
	for _, file := range []*os.File{owner.roots.Work, owner.roots.Admin, owner.roots.Common} {
		if err := workContext.Err(); err != nil {
			return nil, err
		}
		owner.line.writeMu.Lock()
		err := sendStartupAncillary(owner.line.conn, proto.TRequest, raw, file)
		owner.line.writeMu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return nil, err
	}
	if err := owner.line.RejectAncillary(); err != nil {
		return nil, err
	}
	sender, err := owner.line.currentSender()
	if err != nil {
		return nil, err
	}
	if err := owner.peer.checkNativeSender(sender); err != nil {
		return nil, err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != 1 {
		return nil, sessionLeaseError("the actual fixed Git worker returned another response")
	}
	if !response.OK {
		if response.Error != nil {
			return nil, response.Error
		}
		return nil, sessionLeaseError("the actual fixed Git worker returned no result")
	}
	if !api.ExactJSONFields(response.Result, owner.counts) || json.Unmarshal(response.Result, &owner.counts) != nil || owner.counts.Dirty < 0 || owner.counts.Unpushed < 0 {
		return nil, sessionLeaseError("the actual fixed Git worker returned incomplete counts")
	}
	if err := owner.line.WriteFrame(proto.TPing, plan); err != nil {
		return nil, err
	}
	<-owner.waitDone
	if err := errors.Join(owner.waitErr, workContext.Err()); err != nil {
		return nil, err
	}
	owner.complete = true
	if err := owner.Check(ctx, operation, preparation); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func (owner *joinedOrdinaryGitInspectionV2) acceptOriginalWorkerV2(ctx context.Context) error {
	deadline, _ := ctx.Deadline()
	_ = owner.endpoint.listener.SetDeadline(deadline)
	for {
		connection, err := owner.endpoint.listener.AcceptUnix()
		if err != nil {
			return err
		}
		line, err := NewNativeStartupLineV1(connection)
		if err != nil {
			_ = connection.Close()
			return err
		}
		_ = line.SetDeadline(deadline)
		if err := owner.start.checkFrozenV2(ctx); err != nil {
			_ = line.Close()
			return err
		}
		if err := owner.peer.checkLive(); err != nil {
			_ = line.Close()
			return err
		}
		frame, err := line.ReadFrame()
		if err == nil {
			err = owner.acceptOriginalOrdinaryGitHelloV3(ctx, line, frame)
		}
		if err == nil {
			err = line.bindOriginalNativePeerV1(owner.peer)
		}
		if err == nil {
			err = checkNativeHelperExecutableV1(owner.peer, owner.preparation.owner.origin.executableRef, owner.command.Args)
		}
		if err == nil {
			err = owner.endpoint.Check()
		}
		if err == nil {
			err = owner.start.checkFrozenV2(ctx)
		}
		if err != nil {
			_ = line.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A different process cannot acquire this original creation's read
			// loans. Closing its connection never signals that foreign process.
			continue
		}
		owner.mu.Lock()
		owner.line = line
		owner.mu.Unlock()
		hello := proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: ordinaryGitParentRoleV2}
		if owner.purgeImage != nil {
			return writeOrdinaryPurgeImageHelloV3(ctx, line, hello, owner.purgeImage)
		}
		return line.WriteNativeHelloV1(hello)
	}
}

// Check requires original native helper exit and the same current active
// permit after all commands/readers and the original helper Wait have joined.
func (owner *joinedOrdinaryGitInspectionV2) Check(ctx context.Context, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2) error {
	if owner == nil {
		return sessionLeaseError("the joined original Git inspection is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || !owner.complete || owner.waitDone == nil || owner.peer == nil || owner.permit == nil || operation != owner.operation || preparation != owner.preparation {
		return sessionLeaseError("the original Git inspection did not retain its actual native join")
	}
	select {
	case <-owner.waitDone:
	default:
		return sessionLeaseError("the original fixed helper Wait has not joined")
	}
	if err := errors.Join(owner.waitErr, ordinaryPeerExitedV1(owner.peer)); err != nil {
		return err
	}
	return owner.permit.Check(ctx, operation, preparation)
}

// Counts never returns a success-shaped result without its original join and
// current operation/preparation/root checks.
func (owner *joinedOrdinaryGitInspectionV2) Counts(ctx context.Context, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2) (purgeGitInspectionV1, error) {
	if err := owner.Check(ctx, operation, preparation); err != nil {
		return purgeGitInspectionV1{}, err
	}
	return owner.counts, nil
}

// Close cancels only this actual read operation. After private dispatch, the
// original child joins its commands itself; the outside never group-kills it.
func (owner *joinedOrdinaryGitInspectionV2) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	if owner.line != nil {
		_ = owner.line.conn.Close()
	}
	if owner.command != nil && owner.command.Process != nil && !owner.dispatched {
		_ = owner.command.Process.Kill()
	}
	if owner.waitDone != nil {
		<-owner.waitDone
	}
	if owner.command != nil && owner.command.Process != nil {
		if err := owner.checkJoinedHelperProcessV2(); err != nil {
			return err
		}
	}
	// Settle original native endpoint cleanup BEFORE releasing any creator or
	// root loan. An unlink error keeps this SAME helper owner retryable.
	if owner.endpoint != nil {
		if err := owner.endpoint.Close(); err != nil {
			return err
		}
	}
	var failure error
	if owner.line != nil {
		failure = errors.Join(failure, owner.line.Close())
	}
	if owner.peer != nil {
		failure = errors.Join(failure, owner.peer.Close())
	}
	if owner.creation != nil {
		failure = errors.Join(failure, owner.creation.Close())
	}
	if owner.code != nil {
		failure = errors.Join(failure, owner.code.Close())
	}
	if owner.parent != nil {
		failure = errors.Join(failure, owner.parent.Close())
	}
	if owner.roots != nil {
		failure = errors.Join(failure, owner.roots.Close())
	}
	owner.closed = true
	return failure
}
