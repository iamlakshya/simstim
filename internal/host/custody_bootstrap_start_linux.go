//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func (source *managerCustodyColdV1) startBootstrapV1(ctx context.Context, m *Manifest, argv []string) (failure error) {
	launch := &startupLaunchV1{buildingDone: make(chan struct{}), operation: source.operation, manifest: m, executable: source.origin.executableRef, argv: append([]string(nil), argv...), manager: source.manager}
	source.launch = launch
	defer close(launch.buildingDone)
	publication, err := beginRuntimePublicationKind(source.operation.ownerContext, "resume_prepare", nil, source.operation)
	if err != nil {
		return err
	}
	launch.placement = publication
	if err := source.manager.events.armLoadedV1(UnitName(m.ID), source.path, source.oldInvocation); err != nil {
		return err
	}
	source.admission, err = source.reserveBootstrapV1(ctx, m)
	if err != nil {
		return err
	}
	var call *dbus.Call
	err = publication.gateControl(ctx, func(control *placementControl) error {
		if err := source.admission.publishBeforeDispatchV1(ctx, control); err != nil {
			return err
		}
		if err := source.checkPinnedV1(ctx); err != nil {
			return err
		}
		source.admission.dispatched = true
		source.manager.events.markDispatched()
		_ = source.manager.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		defer source.manager.connection.SetWriteDeadline(time.Time{})
		call = source.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(source.manager.ctx, systemdManagerInterface+".StartUnit", 0, make(chan *dbus.Call, 1), UnitName(m.ID), "fail")
		return nil
	})
	launch.started = source.manager.events.wasDispatched()
	if err != nil {
		return err
	}
	call = <-call.Done
	var job dbus.ObjectPath
	if err := call.Store(&job); err != nil {
		switch call.Err.(type) {
		case dbus.Error, *dbus.Error:
			source.manager.events.recordNegativeReply()
		}
		return err
	}
	if !job.IsValid() || job == "/" {
		return sessionLeaseError("the retained service returned no original bootstrap job")
	}
	// Once the real native call was dispatched, cancellation must not erase
	// its first creation handle. Finish the exact correlated job on this same
	// held manager before returning the caller's cancellation to cleanup.
	creationContext, cancelCreation := context.WithTimeout(context.Background(), 30*time.Second)
	launch.unit, err = source.manager.finishSupervisorActivationV1(creationContext, m, source.origin.executableRef, argv, job, 0)
	cancelCreation()
	if err != nil {
		return err
	}
	fd, err := unix.FcntlInt(uintptr(launch.unit.peer.pidfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	source.creation = os.NewFile(uintptr(fd), "original-custody-bootstrap-pidfd")
	source.group, err = openNativeStopCgroupV1(m.ID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := launch.connect(ctx); err != nil {
		return err
	}
	return source.sendBootstrapLoanV1(ctx)
}

func (source *managerCustodyColdV1) sendBootstrapLoanV1(ctx context.Context) (failure error) {
	launch := source.launch
	if launch == nil || launch.loanSent || launch.nativePeer() == nil || launch.nativePeer().identity == nil {
		return sessionLeaseError("the custody bootstrap has no original current native peer")
	}
	if err := launch.check(ctx); err != nil {
		return err
	}
	current, err := source.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err := source.selection.checkCurrentV1(source.operation, current); err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	loan := startupLoanV1{Version: 1, Primary: source.operation.Primary(), Nonce: launch.hello.StartupNonce, Purpose: "manager-custody", Need: startupDigest(current.VMNeedV1), Runtime: startupDigest(runtime)}
	args, err := json.Marshal(loan)
	if err != nil {
		return err
	}
	file, err := source.operation.native.duplicateLockedFile(ctx)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, file.Close()) }()
	if err := launch.placement.gate(ctx, func() error {
		if err := errors.Join(source.admission.checkCurrentV1(ctx), launch.checkNativeMemoryV1(ctx, launch.manifest)); err != nil {
			return err
		}
		launch.loanSent = true
		return launch.line.writeLoan(proto.Request{ID: 1, Op: startupLoanOperationV1, Args: args}, file)
	}); err != nil {
		return err
	}
	result, err := launch.response(ctx, 1, true)
	if err != nil {
		return err
	}
	var reply struct {
		Nonce string `json:"startup_nonce"`
	}
	if !api.ExactJSONFields(result, reply) || json.Unmarshal(result, &reply) != nil || reply.Nonce != loan.Nonce {
		return sessionLeaseError("the actual custody bootstrap acknowledged another original loan")
	}
	return launch.check(ctx)
}

func (source *custodyKernelSourceV1) captureRolesV1(ctx context.Context) error {
	deadline := time.Now().Add(10 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = source.launch.line.SetDeadline(deadline)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = source.launch.line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	roles := []string{"custody-root-v1", "custody-holder-v1", "custody-cgroup-v1", "custody-pidfd-v1", "custody-claim-v1"}
	for index, role := range roles {
		id := uint64(index + 20)
		if err := source.launch.line.WriteFrame(proto.TRequest, proto.Request{ID: id, Op: "manager_custody_role_v1"}); err != nil {
			return err
		}
		frame, err := source.launch.line.ReadFrame()
		if err != nil {
			return err
		}
		var response proto.Response
		if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !response.OK {
			return errors.Join(sessionLeaseError("the actual bootstrap supplied no exact inherited native role"), source.launch.line.RejectAncillary())
		}
		var result struct {
			Role string `json:"role"`
		}
		if !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, &result) != nil || result.Role != role {
			return errors.Join(sessionLeaseError("the original bootstrap changed an inherited role boundary"), source.launch.line.RejectAncillary())
		}
		file, sender, err := source.launch.line.takeLoanFile()
		if err != nil {
			return err
		}
		if err := source.launch.nativePeer().checkNativeSender(sender); err != nil {
			return errors.Join(err, file.Close())
		}
		source.roles[role] = file
	}
	return nil
}
