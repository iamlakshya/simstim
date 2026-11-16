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
	operation *SessionOperation
	before    Limits
	after     Limits
	need      []byte
	runtime   []byte
	used      bool
}

// admitRestartMemoryV1 captures the actual outside request under the SAME OP
// and placement gate before stop or publication. Prospective Manifest values
// cannot issue this admission. Zero preserves the protected cap and intent.
func admitRestartMemoryV1(ctx context.Context, operation *SessionOperation, requested int64) error {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("restart memory has no original owning operation")
	}
	if err := errors.Join(CheckCaller(), ctx.Err(), checkMemoryMax(requested)); err != nil {
		return err
	}
	if requested < 0 && requested != api.MemoryAuto {
		return UserError("memory_max", "the restart memory request is not a supported size or auto", "Choose auto or a supported explicit memory size.")
	}
	return withPlacement(ctx, false, func(_ *placementControl) error {
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
		budget := DefaultMemoryMax()
		if budget <= 0 {
			return UserError("memory_max", "the host's exact session memory budget is unavailable", "Inspect the host's memory facts before restarting this session.")
		}
		after := current.Limits
		switch {
		case requested == api.MemoryAuto:
			after.MemoryMax, after.MemoryAsked = budget, 0
		case requested > 0:
			after.MemoryMax, after.MemoryAsked = requested, requested
		}
		if after.MemoryMax <= 0 || after.MemoryMax > budget {
			return UserError("memory_max", "the exact requested restart cap exceeds the host's admitted session budget", "Choose a smaller explicit size, or resize the host before restarting.")
		}
		if len(current.VMNeedV1) != 0 && !reflect.DeepEqual(after, current.Limits) {
			return vmNeedError("a VM memory change needs a fresh authenticated client Need before restart")
		}
		runtime, err := json.Marshal(current.RuntimeManifest)
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
		operation.restartMemory = &restartMemoryAdmissionV1{operation: operation, before: current.Limits, after: after, need: bytes.Clone(current.VMNeedV1), runtime: runtime}
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
		!bytes.Equal(receipt.need, before.VMNeedV1) || !bytes.Equal(receipt.runtime, runtime) {
		return sessionLeaseError("the restart memory writer differs from its original admitted request")
	}
	budget := DefaultMemoryMax()
	if budget <= 0 || receipt.after.MemoryMax <= 0 || receipt.after.MemoryMax > budget {
		return UserError("memory_max", "the admitted restart cap no longer fits this host's session budget", "Inspect the host memory and prepare the restart again.")
	}
	receipt.used = true
	return nil
}
