//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// StoppedSessionV1 retains one actual supervised whole-stop. Only the
// supported live native owner can construct it. It has no data constructor,
// PID/path getter, execution grant or force-work-loss option.
type StoppedSessionV1 struct {
	noneHolder                *noneWarmHolderV1 // live None/N5 only; never whole-stop permission
	features                  []string          // current original hello capabilities, never authority
	operation                 *SessionOperation
	origin                    *placement
	manager                   *nativeUserManagerV1
	invocation                *nativeStopInvocationV1
	holder                    *sessionFileLease
	line                      *StartupLineV1
	source                    Manifest
	state                     proto.NativeStopStateV1
	boot                      string
	stopSent, settled, closed bool
	holderHanded              bool // only the same bound Cold's actual native dispatch consumes it
	mu                        sync.Mutex
}

// StopSessionForOperationV1 stops only the actual current supported supervisor
// captured through its socket, current sender and native invocation. Unknown
// already-stopped or unmanaged current-boot lifetimes are preserved/refused.
// Callers separately authorise loss of unreviewed work before calling this.
func StopSessionForOperationV1(ctx context.Context, operation *SessionOperation) (_ *StoppedSessionV1, returned error) {
	owner, err := captureSupportedSupervisorV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	arguments := proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: owner.state.Nonce}

	// From this actual attempt onwards, every failure keeps the same native
	// source until physical settlement; EOF is never interpreted as exit.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner.stopSent = true
	var accepted proto.NativeStopStateV1
	err = owner.nativeStopRequestV1(ctx, 2, proto.NativeStopCommitV1, arguments, &accepted)
	if err == nil && accepted != owner.state {
		err = auxiliaryUnavailableV1("the actual native stop accepted another original continuation")
	}
	if settleErr := owner.settleNativeStopV1(); settleErr != nil {
		return nil, errors.Join(err, settleErr)
	}
	// A lost ACK stays a transport error. Independently proven physical
	// closure can still be returned as its opaque native source; it is never a
	// local review or pull-finish acknowledgement.
	if checkErr := owner.Check(context.Background(), operation); checkErr != nil {
		return nil, errors.Join(err, checkErr)
	}
	complete = true
	return owner, err
}

func captureSupportedSupervisorV1(ctx context.Context, operation *SessionOperation) (*StoppedSessionV1, error) {
	return captureSupportedSupervisorPurposeV1(ctx, operation, proto.NativeStopInspectV1)
}
func captureSupportedSupervisorPurposeV1(ctx context.Context, operation *SessionOperation, inspect string) (_ *StoppedSessionV1, returned error) {
	if inspect != proto.NativeStopInspectV1 && inspect != proto.NativeResumeInspectV1 {
		return nil, auxiliaryUnavailableV1("the original native inspection purpose is unsupported")
	}
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, auxiliaryUnavailableV1("whole-stop has no original owning operation")
	}
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return nil, err
	}
	saved, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if saved.Wall == nil || saved.SandboxOf != "" || saved.Auxiliary != nil || saved.Run.BootID != BootID() || !auxiliaryBootIDV1(BootID()) {
		return nil, auxiliaryUnavailableV1("the selected session has no supported current native supervisor; preserve its unknown lifetime")
	}
	origin, err := placementForCall()
	if err != nil {
		return nil, err
	}
	if err := origin.checkIdentity(); err != nil {
		return nil, err
	}
	owner := &StoppedSessionV1{operation: operation, origin: origin, source: CloneManifest(*saved), boot: BootID()}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	owner.manager, err = openNativeUserManagerV1(ctx)
	if err != nil {
		if !(errors.Is(err, os.ErrNotExist) && inspect == proto.NativeResumeInspectV1 && IsNoneSupervisor(saved)) {
			return nil, err
		}
	}
	auxiliary, err := AuxUnitNameV1(operation.Primary())
	if err != nil {
		return nil, err
	}
	if owner.manager != nil {
		if err := owner.manager.requireAuxiliaryUnitAbsentV1(ctx, auxiliary); err != nil {
			return nil, err
		}
		aux, err := readAuxMemoryUnitV1(ctx, operation.Primary())
		if err != nil || aux.exists {
			return nil, errors.Join(auxiliaryUnavailableV1("an auxiliary native lifetime has no matching original settlement"), err)
		}
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket(operation.Primary()))
	if err != nil {
		return nil, errors.Join(auxiliaryUnavailableV1("the current-boot session has no actual supported live supervisor connection"), err)
	}
	owner.line, err = NewNativeStartupLineV1(connection)
	if err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	if owner.manager == nil {
		owner.invocation, err = captureNativeNoneWarmV1(ctx, connection, operation.Primary(), origin.executableRef)
	} else {
		owner.invocation, err = captureNativeStopInvocationV1(ctx, owner.manager, connection, operation.Primary(), origin.executableRef, saved.Limits.MemoryMax)
	}
	if err != nil {
		return nil, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	arguments := proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: hex.EncodeToString(nonce[:])}
	if deadline, ok := ctx.Deadline(); ok {
		_ = owner.line.SetDeadline(deadline)
	} else {
		_ = owner.line.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if err := owner.line.WriteFrame(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		return nil, err
	}
	helloFrame, err := owner.line.ReadFrame()
	if err != nil {
		return nil, err
	}
	if err := owner.line.RejectAncillary(); err != nil {
		return nil, err
	}
	var hello proto.Hello
	if helloFrame.Type != proto.THello || json.Unmarshal(helloFrame.Payload, &hello) != nil || hello.Check(version.Protocol) != nil || hello.Role != "supervisor" || !slices.Contains(hello.Features, proto.FeatureNativeStopV1) {
		return nil, auxiliaryUnavailableV1("the actual supervisor cannot prove its original native whole-stop continuation")
	}
	if inspect == proto.NativeResumeInspectV1 && !slices.Contains(hello.Features, proto.FeatureNativeResumeV1) {
		return nil, auxiliaryUnavailableV1("the actual supervisor cannot retain an original native warm resume")
	}
	owner.features = slices.Clone(hello.Features)
	if owner.manager == nil && !slices.Contains(hello.Features, proto.FeatureNoneSupervisorV1) {
		return nil, sessionLeaseError("the current unmanaged supervisor has no original unwalled warm producer")
	}
	if err := owner.invocation.checkCreationV1(ctx); err != nil {
		return nil, err
	}
	if err := owner.nativeStopRequestV1(ctx, 1, inspect, arguments, &owner.state); err != nil {
		return nil, err
	}
	if inspect == proto.NativeResumeInspectV1 && NoneSupervisorShapeV1(saved) {
		err = owner.receiveNativeNoneHolderProofV1(ctx, 1, inspect)
	} else {
		err = owner.receiveNativeHolderProofV1(1)
	}
	if err != nil {
		return nil, err
	}
	runtimeBytes, err := startupRuntimeBytes(saved)
	if err != nil {
		return nil, err
	}
	if owner.state.Version != 1 || owner.state.Primary != operation.Primary() || owner.state.Nonce != arguments.Nonce || owner.state.Generation == "" || owner.state.Generation != saved.Run.Generation || owner.state.Need != startupDigest(saved.VMNeedV1) || owner.state.Runtime != startupDigest(runtimeBytes) {
		return nil, auxiliaryUnavailableV1("the current native supervisor differs from the original protected source")
	}
	if err := owner.invocation.Check(ctx); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func (owner *StoppedSessionV1) checkCurrentStopSenderV1(ctx context.Context) error {
	sender, err := owner.line.currentSender()
	if err != nil {
		return err
	}
	return errors.Join(owner.invocation.peer.checkNativeSender(sender), owner.invocation.Check(ctx))
}
func (owner *StoppedSessionV1) nativeStopRequestV1(ctx context.Context, id uint64, op string, args, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = owner.line.SetDeadline(deadline)
	} else {
		_ = owner.line.SetDeadline(time.Now().Add(10 * time.Second))
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = owner.line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if err := owner.line.WriteFrame(proto.TRequest, proto.Request{ID: id, Op: op, Args: raw}); err != nil {
		return err
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return err
	}
	if op == proto.NativeStopInspectV1 || op == proto.NativeResumeInspectV1 {
		if err := owner.line.acceptNativeStopIdentityV1(frame); err != nil {
			return err
		}
		if err := owner.line.bindOriginalNativePeerV1(owner.invocation.peer); err != nil {
			return err
		}
	} else if err := owner.line.RejectAncillary(); err != nil {
		return err
	}
	if err := owner.checkCurrentStopSenderV1(ctx); err != nil {
		return err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !response.OK {
		if response.Error != nil {
			return response.Error
		}
		return auxiliaryUnavailableV1("the actual supervisor refused its original stop continuation")
	}
	if out != nil {
		if !api.ExactJSONFields(response.Result, proto.NativeStopStateV1{}) {
			return auxiliaryUnavailableV1("the original native stop state has unknown fields")
		}
		return json.Unmarshal(response.Result, out)
	}
	return nil
}

// Check requires the SAME original OP/root plus native physical facts. No
// copied public correlation, boolean, status row or later PID lookup can pass.
func (owner *StoppedSessionV1) Check(ctx context.Context, operation *SessionOperation) error {
	if owner == nil || ctx == nil || operation == nil {
		return auxiliaryUnavailableV1("the original physical whole-stop owner is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner == nil || ctx == nil || operation == nil || operation != owner.operation || !owner.settled || owner.closed || owner.holderHanded || owner.holder == nil || owner.invocation == nil || owner.origin == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return auxiliaryUnavailableV1("the original physical whole-stop owner is absent or ended")
	}
	if BootID() != owner.boot {
		return auxiliaryUnavailableV1("the native epoch changed during whole-stop ownership")
	}
	if err := errors.Join(CheckCaller(), owner.origin.checkIdentity(), owner.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.holder.root)); err != nil {
		return err
	}
	fresh, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(fresh.VMNeedV1, owner.source.VMNeedV1) || !reflect.DeepEqual(fresh.RuntimeManifest, owner.source.RuntimeManifest) || fresh.Auxiliary != nil {
		return auxiliaryUnavailableV1("the original protected whole-stop source changed")
	}
	return owner.checkStoppedNativeFactsV1(ctx)
}
func (owner *StoppedSessionV1) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	if owner.stopSent && !owner.settled {
		if err := owner.settleNativeStopV1(); err != nil {
			return err
		}
	}
	owner.closed = true
	var failure error
	if owner.line != nil {
		failure = errors.Join(failure, owner.line.Close())
	}
	if owner.holder != nil {
		failure = errors.Join(failure, owner.holder.Close())
	}
	if owner.invocation != nil {
		failure = errors.Join(failure, owner.invocation.Close())
	}
	if owner.manager != nil {
		failure = errors.Join(failure, owner.manager.Close())
	}
	return failure
}
