package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

type restartMemoryAdmissionV1 struct {
	operation    *SessionOperation
	before       Limits
	after        Limits
	need         []byte
	runtime      []byte
	nextNeed     []byte
	nextRuntime  []byte
	beforeAgents string
	nextAgents   string
	vm           *resumeVMSelectionV1
	used         bool
	restore      bool
	policyMemory *string
	checkPolicy  bool
}

// admitRestartMemoryV1 captures the actual outside request under the SAME OP
// and placement gate before stop or publication. Prospective Manifest values
// cannot issue this admission. Zero preserves the protected cap and intent.
func admitRestartMemoryV1(ctx context.Context, operation *SessionOperation, requested int64, fresh ...[]api.Layer) error {
	if len(fresh) > 1 {
		return memberError("the restart supplied more than one original layer vector")
	}
	var layers []api.Layer
	if len(fresh) == 1 {
		layers = api.CloneLayers(fresh[0])
	}
	return admitRestartMemoryPurposeV1(ctx, operation, requested, layers, nil, false)
}

// This actual manual caller supplies its original Args. Only size policy
// inputs are copied; no secret values enter the private memory receipt.
func admitRestartMemoryRequestV1(ctx context.Context, operation *SessionOperation, args api.ResumeArgs) error {
	selection, err := resumeVMSelectionForArgsV1(ctx, operation, args)
	if err != nil {
		return err
	}
	request := captureRestartSizeRequestV1(args)
	request.vm = selection
	return admitRestartMemoryPurposeV1(ctx, operation, args.MemoryMax, nil, request, false)
}

// Only the actual automatic restore controller calls this private producer.
// It skips the specified drain precheck, never installation/purge identity,
// native accounting, the original OP or the final atomic writer.
func admitRestoreMemoryV1(ctx context.Context, operation *SessionOperation) error {
	return admitRestartMemoryPurposeV1(ctx, operation, 0, nil, nil, true)
}

func admitRestartMemoryPurposeV1(ctx context.Context, operation *SessionOperation, requested int64, layers []api.Layer, request *restartSizeRequestV1, restore bool) error {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("restart memory has no original owning operation")
	}
	if err := errors.Join(CheckCaller(), ctx.Err(), checkMemoryMax(requested)); err != nil {
		return err
	}
	if requested < 0 && requested != api.MemoryAuto {
		return UserError("memory_max", "the restart memory request is not a supported size or auto", "Choose auto or a supported explicit memory size.")
	}
	return withPlacement(ctx, restore, func(control *placementControl) error {
		// Only the actual typed automatic controller sets this private purpose.
		if !restore {
			if err := checkUserDrainLockedV1(ctx, control); err != nil {
				return err
			}
		}
		current, err := operation.LoadContext(ctx)
		if err != nil {
			return err
		}
		if current.ID != operation.Primary() || current.path != "" || current.SandboxOf != "" {
			return memberError("restart memory does not name its original protected primary")
		}
		if err := ValidateManifestStart(current); err != nil {
			return err
		}
		if request != nil && request.vm != nil && !sameRestartExecutionSelectionV1(current, &request.vm.before) {
			return newRuntimeError("the resume selection changed its original protected source before admission")
		}
		preview, err := restartSizePreviewV1(current, layers, request)
		if err != nil {
			return err
		}
		after, err := restartLimitsForRequestV1(current, &preview, requested, restore)
		if err != nil {
			return err
		}
		if request != nil && request.vm != nil {
			if err := request.vm.checkLockedV1(ctx, control); err != nil {
				return err
			}
		}
		if isWalledMemoryV1(current) {
			// This is only a prospective replacement check. The current
			// canonical reservation remains intact and every other start still
			// counts it until the old native owner has physically ended.
			if _, err := memoryRoomLockedV1(ctx, operation, after.Declared); err != nil {
				return err
			}
		}
		if !bytes.Equal(current.VMNeedV1, preview.VMNeedV1) && (request == nil || request.vm == nil) {
			return vmNeedError("a changed VM selection has no original cold preparation")
		}
		runtime, err := json.Marshal(current.RuntimeManifest)
		if err != nil {
			return err
		}
		nextRuntime, err := json.Marshal(preview.RuntimeManifest)
		if err != nil {
			return err
		}
		operation.gate.Lock()
		defer operation.gate.Unlock()
		if !operation.active.Load() || operation.startup != nil || operation.restartMemory != nil {
			return sessionLeaseError("restart memory was already admitted or the original operation ended")
		}
		if err := operation.native.check(ctx); err != nil {
			return err
		}
		operation.restartMemory = &restartMemoryAdmissionV1{operation: operation, before: current.Limits, after: after, need: bytes.Clone(current.VMNeedV1), runtime: runtime, nextNeed: bytes.Clone(preview.VMNeedV1), nextRuntime: nextRuntime, beforeAgents: current.Agents, nextAgents: preview.Agents, restore: restore}
		if request != nil {
			operation.restartMemory.vm = request.vm
			operation.restartMemory.checkPolicy = true
			if preview.Policy != nil && preview.Policy.Sandbox.Memory != nil {
				value := *preview.Policy.Sandbox.Memory
				operation.restartMemory.policyMemory = &value
			}
		}
		return nil
	}, false)
}

func consumeRestartMemoryV1(operation *SessionOperation, before, after *Manifest) error {
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.startup != nil {
		return sessionLeaseError("the restart writer lost its original owning scope")
	}
	receipt := operation.restartMemory
	if receipt == nil {
		if !reflect.DeepEqual(before.Limits, after.Limits) {
			return sessionLeaseError("a prospective manifest cannot issue a changed restart memory cap")
		}
		return nil
	}
	runtime, err := json.Marshal(before.RuntimeManifest)
	if err != nil {
		return err
	}
	if receipt.used || receipt.operation != operation || !reflect.DeepEqual(receipt.before, before.Limits) || !reflect.DeepEqual(receipt.after, after.Limits) ||
		!bytes.Equal(receipt.need, before.VMNeedV1) || !bytes.Equal(receipt.runtime, runtime) || before.Agents != receipt.beforeAgents || !bytes.Equal(receipt.nextNeed, after.VMNeedV1) || after.Agents != receipt.nextAgents {
		return sessionLeaseError("the restart memory writer differs from its original admitted request")
	}
	nextRuntime, err := json.Marshal(after.RuntimeManifest)
	if err != nil || !bytes.Equal(receipt.nextRuntime, nextRuntime) {
		return sessionLeaseError("the restart runtime differs from its original admitted selection")
	}
	if receipt.checkPolicy {
		var memory *string
		if after.Policy != nil {
			memory = after.Policy.Sandbox.Memory
		}
		if !reflect.DeepEqual(memory, receipt.policyMemory) {
			return sessionLeaseError("the restart size policy changed after its original request admission")
		}
	}
	budget := DefaultMemoryMax()
	if isWalledMemoryV1(after) && (budget <= 0 || receipt.after.MemoryMax <= 0 || receipt.after.MemoryMax > budget || receipt.after.Declared != receipt.after.MemoryMax) {
		return UserError("memory_max", "the admitted restart cap no longer fits this host's session budget", "Inspect the host memory and prepare the restart again.")
	}
	receipt.used = true
	return nil
}
