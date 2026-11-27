//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// This input exists only inside the actual separate nineteen-role receiver.
// Its native OP/current parent/code/namespace handshake issues the class;
// neither old descriptors nor Base loan DATA can construct it independently.
type originalSlotStartupV2 struct {
	mu                  sync.Mutex
	issuer              *originalSlotStartupV2
	incoming            *slotReservedStartupIncomingV2
	boot                *originalBootV1
	operation           *SessionOperation
	roles               []*os.File
	attempted, released bool
	closeErr            error
}

func acceptSlotReservedStartupIncomingV2(ctx context.Context, incoming *slotReservedStartupIncomingV2) (_ *SessionOperation, returned error) {
	if incoming == nil {
		return nil, sessionLeaseError("the reserved startup has no actual incoming producer")
	}
	incoming.mu.Lock()
	defer incoming.mu.Unlock()
	// owner.mu is held by the actual AcceptSlotReservedStartupV2 caller.
	if err := incoming.checkOriginalV2(ctx); err != nil {
		return nil, err
	}
	if len(incoming.files) != 19 {
		return nil, sessionLeaseError("the reserved startup supplied another exact native layout")
	}
	for _, file := range incoming.files {
		if file == nil {
			return nil, sessionLeaseError("the original reserved startup lost a native operand")
		}
	}
	if incoming.preparationFrame.Type != incoming.preparation.preparationFrame.Type || !bytes.Equal(incoming.preparationFrame.Payload, incoming.preparation.preparationFrame.Payload) {
		return nil, sessionLeaseError("the reserved startup substituted its captured preparation frame")
	}
	var envelope proto.Request
	if json.Unmarshal(incoming.loanFrame.Payload, &envelope) != nil {
		return nil, sessionLeaseError("the original reserved startup has no bounded loan envelope")
	}
	var args slotReservedStartupLoanV2
	if json.Unmarshal(envelope.Args, &args) != nil {
		return nil, sessionLeaseError("the original reserved startup has no exact loan data")
	}
	loan := args.Base
	if loan.Version != 1 || loan.Purpose != "ordinary" && loan.Purpose != "automatic" || loan.Purpose == "ordinary" && loan.Automatic != "" {
		return nil, sessionLeaseError("the reserved startup changed its original bounded intent")
	}
	if err := errors.Join(incoming.pending.peer.CheckSender(incoming.sender), checkBorrowedFlockV1(incoming.files[0], incoming.sender)); err != nil {
		return nil, err
	}
	placement, err := admittedBootPlacementV1(ctx)
	if err != nil {
		return nil, err
	}
	native, err := adoptStartupOperationFile(ctx, loan.Primary, incoming.files[0])
	if err != nil {
		return nil, err
	}
	owner := incoming.owner
	operation := sessionOperationFromNative(owner.Lifetime(), loan.Primary, native)
	input := &originalSlotStartupV2{incoming: incoming, operation: operation, roles: incoming.files[1:]}
	input.issuer = input
	boot := &originalBootV1{placement: placement, owner: owner, operation: operation, line: incoming.line, loan: loan, slotReservation: input}
	input.boot = boot
	operation.originalBoot = boot
	// The same actual partial OP owns all received rights before any later
	// validation. The supervisor adopts it even alongside a returned error.
	incoming.used, incoming.files = true, nil
	fail := func(err error) (*SessionOperation, error) { return operation, errors.Join(err, input.closeRolesV2()) }
	if err := input.checkClosedOperandsV2(ctx); err != nil {
		return fail(err)
	}
	saved, err := operation.LoadContext(ctx)
	if err != nil {
		return fail(err)
	}
	runtimeBytes, err := startupRuntimeBytes(saved)
	if err != nil {
		return fail(err)
	}
	if saved.ID != loan.Primary || saved.Wall == nil || saved.Wall.Level != "microvm" || saved.Status.State != StateStarting || startupDigest(saved.VMNeedV1) != loan.Need || startupDigest(runtimeBytes) != loan.Runtime {
		return fail(sessionLeaseError("the reserved startup changed its protected original VM source"))
	}
	need, err := vmneed.DecodeV1(saved.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return fail(err)
	}
	if err := need.ValidateWall(runtime.GOARCH, saved.Wall.Level, saved.Wall.Driver, saved.Limits.MemoryMax); err != nil {
		return fail(err)
	}
	if saved.RuntimeManifest == nil || !reflect.DeepEqual(*saved.RuntimeManifest, need.Runtime) {
		return fail(sessionLeaseError("the reserved startup lost its original complete runtime selection"))
	}
	info, err := input.roles[9].Stat()
	if err != nil || info.Size() != need.DiskMax {
		return fail(errors.Join(sessionLeaseError("the original reserved disk changed its exact admitted length"), err))
	}
	if loan.Purpose == "automatic" {
		if !supply.IsHex64(loan.Automatic) || startupDigest(saved.AutomaticRestore) != loan.Automatic {
			return fail(automaticRestoreError())
		}
		if _, err := ValidateAutomaticRestore(saved); err != nil {
			return fail(err)
		}
		operation.automaticBoot = &AutomaticBootOperationV1{operation: operation, plan: bytes.Clone(saved.AutomaticRestore)}
	} else if len(saved.AutomaticRestore) != 0 {
		return fail(automaticRestoreError())
	}
	boot.need, boot.currentNeed, boot.policyNext = bytes.Clone(saved.VMNeedV1), bytes.Clone(saved.VMNeedV1), bytes.Clone(saved.PolicyNext)
	copy := cloneSelectedRuntime(*saved.RuntimeManifest)
	boot.runtime = &copy
	if err := errors.Join(incoming.pending.peer.CheckSender(incoming.sender), checkBorrowedFlockV1(native.file, incoming.sender), native.check(ctx), ctx.Err()); err != nil {
		return fail(err)
	}
	incoming.line.mu.Lock()
	defer incoming.line.mu.Unlock()
	if incoming.line.closed || incoming.line.candidate != incoming.pending || incoming.line.slotStartupPending != incoming.preparation || owner.pending != nil || owner.startup != nil {
		return fail(sessionLeaseError("the exact reserved startup candidate ended before its native commit"))
	}
	owner.pending, owner.startup = incoming.pending, boot
	return operation, nil
}

func (input *originalSlotStartupV2) checkClosedOperandsV2(ctx context.Context) error {
	if input == nil || input.issuer != input || input.incoming == nil || !input.incoming.used || input.boot == nil || input.boot.slotReservation != input || input.operation == nil || input.operation.originalBoot != input.boot || len(input.roles) != 18 || input.released {
		return sessionLeaseError("the original reserved startup input ended or was substituted")
	}
	for _, index := range []int{0, 1, 14} {
		if _, err := slotSnapshotPIDV1(input.roles[index], false); err != nil {
			return err
		}
	}
	for _, index := range []int{2, 13} {
		if err := slotSnapshotGroupV1(input.roles[index]); err != nil {
			return err
		}
		empty, err := originalStopCgroupEmptyV1(input.roles[index])
		if err != nil || !empty {
			return errors.Join(sessionLeaseError("the original reserved slot has no positive native whole-group closure"), err)
		}
	}
	info, err := input.roles[9].Stat()
	if err != nil {
		return err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || native.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o660 {
		return sessionLeaseError("the original reserved disk has another actual native file role")
	}
	return errors.Join(ctx.Err(), slotlaunch.CheckOriginalReservationLeaseV2(input.roles[17]))
}

func (input *originalSlotStartupV2) closeRolesV2() error {
	if input.released {
		return input.closeErr
	}
	if input.closeErr != nil {
		return input.closeErr
	}
	for _, file := range input.roles {
		if file != nil {
			input.closeErr = errors.Join(input.closeErr, file.Close())
		}
	}
	input.released = input.closeErr == nil
	return input.closeErr
}
