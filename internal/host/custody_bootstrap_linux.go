//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ManagerCustodyPendingV1 reports the actual inherited bootstrap state.
// It supplies no source or permission to create a runtime.
func (owner *OwnedSupervisorLease) ManagerCustodyPendingV1() bool {
	return owner != nil && owner.custody != nil && !owner.custody.released.Load() && !owner.custody.closed.Load()
}

// CheckManagerCustodyRequesterV1 checks the actual accepted borrower's native
// lifetime while bootstrap waits. It creates no loan and consumes no claim.
func (owner *OwnedSupervisorLease) CheckManagerCustodyRequesterV1(ctx context.Context) error {
	if owner == nil || owner.custody == nil {
		return sessionLeaseError("the custody bootstrap has no inherited owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup == nil || owner.pending == nil || owner.pending.peer == nil || owner.startup.loan.Purpose != "manager-custody" || !owner.Valid(owner.native.id) || !owner.startup.operation.Valid(owner.native.id) {
		return sessionLeaseError("the custody bootstrap lost its actual original requester")
	}
	return errors.Join(ctx.Err(), owner.pending.peer.Check())
}

// HandleManagerCustodyBootstrapV1 serves only the same authenticated startup
// line and actual current OP. Passive proof lends each original kernel object;
// only the final claim consumes the closed peer's one queued packet.
func (owner *OwnedSupervisorLease) HandleManagerCustodyBootstrapV1(ctx context.Context, line *StartupLineV1, frame proto.Frame) (committed bool, failure error) {
	if owner == nil || owner.custody == nil || !owner.ManagerCustodyPendingV1() {
		return false, sessionLeaseError("this main has no actual inherited custody bootstrap")
	}
	if err := owner.CheckStartupFrame(line); err != nil {
		return false, err
	}
	var request proto.Request
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, proto.Request{}) || json.Unmarshal(frame.Payload, &request) != nil || request.ID == 0 {
		return false, sessionLeaseError("the custody bootstrap received an invalid current-owner request")
	}
	owner.mu.Lock()
	boot := owner.startup
	validBoot := boot != nil && boot.line == line && boot.loan.Purpose == "manager-custody" && boot.operation != nil && boot.operation.Valid(owner.native.id)
	owner.mu.Unlock()
	if !validBoot {
		return false, sessionLeaseError("the custody bootstrap lost the SAME original operation")
	}
	inherited := owner.custody
	inherited.mu.Lock()
	defer inherited.mu.Unlock()
	if request.Op == "manager_custody_abort_v1" && request.ID == 29 && len(request.Args) == 0 {
		var err error
		if inherited.consumed.Load() {
			err = owner.checkCustodyReleaseV1(ctx, boot, inherited)
		} else {
			err = inherited.check(ctx)
		}
		if err != nil {
			return false, err
		}
		return false, errors.Join(ErrManagerCustodyPassiveAbortV1, line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, OK: true}))
	}
	if inherited.consumed.Load() {
		if request.Op != "manager_custody_release_v1" || request.ID != 31 || len(request.Args) != 0 {
			return false, sessionLeaseError("the claimed bootstrap accepts only its original final release")
		}
		if err := owner.checkCustodyReleaseV1(ctx, boot, inherited); err != nil {
			return false, err
		}
		inherited.released.Store(true)
		return true, line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, OK: true})
	}
	if err := inherited.check(ctx); err != nil {
		return false, err
	}
	if request.Op == "manager_custody_role_v1" && len(request.Args) == 0 && request.ID >= 20 && request.ID < 20+custodyBundleSizeV1 {
		roles := []string{"custody-root-v1", "custody-holder-v1", "custody-cgroup-v1", "custody-pidfd-v1", "custody-claim-v1"}
		role := roles[request.ID-20]
		raw, err := json.Marshal(proto.Response{ID: request.ID, OK: true, Result: json.RawMessage(`{"role":"` + role + `"}`)})
		if err != nil {
			return false, err
		}
		line.writeMu.Lock()
		err = sendStartupAncillary(line.conn, proto.TResponse, raw, inherited.roles()[role])
		line.writeMu.Unlock()
		return false, err
	}
	if request.Op != "manager_custody_claim_v1" || request.ID != 30 {
		return false, sessionLeaseError("the custody bootstrap received an unsupported original-owner operation")
	}
	m, err := boot.operation.LoadContext(ctx)
	if err != nil || m.Status.State != StateStarting {
		return false, errors.Join(sessionLeaseError("the custody claim has no final admitted restart"), err)
	}
	selection, err := owner.checkCustodyFinalSelectionV1(ctx, boot, m, request.Args)
	if err != nil {
		return false, err
	}
	config, err := inherited.manager.lockCustodyConfigV1(ctx)
	if err != nil {
		return false, err
	}
	defer config.Close()
	if err := inherited.check(ctx); err != nil {
		return false, err
	}
	// This is the irreversible one-shot boundary. An error after it leaves a
	// consumed claim in the retained bundle; no retry can create another owner.
	if err := inherited.claim.consume(); err != nil {
		return false, err
	}
	inherited.consumed.Store(true)
	for role := range inherited.roles() {
		if err := inherited.manager.notifyNativeV1(ctx, "FDSTOREREMOVE=1\nFDNAME="+role, nil); err != nil {
			return false, err
		}
	}
	if err := inherited.manager.notifyNativeBarrierV1(ctx); err != nil {
		return false, err
	}
	entries, err := inherited.manager.dumpCustodyStoreV1(ctx, UnitName(owner.native.id))
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, sessionLeaseError("the original manager retained aliases after the custody claim")
	}
	if err := errors.Join(owner.native.check(ctx), checkCustodyFlockV1(owner.native.file), inherited.manager.checkCustodyPinV1(ctx, UnitName(owner.native.id))); err != nil {
		return false, err
	}
	owner.mu.Lock()
	if owner.startup != boot || boot.operation == nil || !boot.operation.Valid(owner.native.id) {
		owner.mu.Unlock()
		return false, sessionLeaseError("the final custody selection lost its original accepted operation")
	}
	boot.need, boot.currentNeed = append([]byte(nil), m.VMNeedV1...), append([]byte(nil), m.VMNeedV1...)
	boot.runtime = CloneManifest(*m).RuntimeManifest
	boot.loan.Need, boot.loan.Runtime = selection.Need, selection.Runtime
	owner.mu.Unlock()
	// The same original OFD becomes this real main's owning lease only after
	// the kernel claim and manager-alias removal. Never unlock or reopen it.
	owner.native.mu.Lock()
	owner.native.borrowed = false
	owner.native.mu.Unlock()
	return false, line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, OK: true})
}
