//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

// probeReservedGvisorV1 consumes the actual New witness's delivered runtime,
// never an invented sandbox.probe DTO or a saved inventory. It does not consume
// the future original boot loan. The actual native start still needs that loan.
func probeReservedGvisorV1(ctx context.Context, operation *SessionOperation) (wall.Probe, error) {
	if ctx == nil || operation == nil {
		return wall.Probe{}, newRuntimeError("the gvisor preflight has no actual request lifetime")
	}
	var result wall.Probe
	err := operation.withCurrentRead(ctx, func(owned context.Context) error {
		var err error
		result, err = probeReservedGvisorOwnedV1(owned, operation)
		return err
	})
	return result, err
}

func probeReservedGvisorOwnedV1(ctx context.Context, operation *SessionOperation) (wall.Probe, error) {
	witness, _ := ctx.Value(newRequestKeyV1{}).(*newRequestWitnessV1)
	if witness == nil || operation == nil || !operation.Valid(operation.Primary()) {
		return wall.Probe{}, newRuntimeError("the gvisor preflight has no original reserved request")
	}
	witness.mu.Lock()
	if !witness.captured || witness.bound != operation || witness.bridge == nil || witness.requestID == 0 || witness.runtime == nil || witness.bridge.ctx.Err() != nil {
		witness.mu.Unlock()
		return wall.Probe{}, newRuntimeError("the gvisor preflight lost its actual New producer")
	}
	b, requestID, selected := witness.bridge, witness.requestID, witness.runtime
	witness.mu.Unlock()
	manifest, err := selected.inventory(ctx)
	if err != nil {
		return wall.Probe{}, err
	}
	key, err := selectedRuntimeKey(manifest)
	if err != nil {
		return wall.Probe{}, err
	}
	probeContext, finish, err := newGvisorProbePurposeV1(ctx, b, requestID, selected, manifest)
	if err != nil {
		return wall.Probe{}, err
	}
	purpose := gvisorProbeFromContextV1(probeContext)
	for _, cached := range cachedWallProbes(ctx, selected.publication.owner) {
		if cachedGvisorMatchesV1(cached, key, purpose.physicalKey) {
			// Recheck the held original objects after reading the cached publication.
			if err := errors.Join(purpose.check(probeContext), finish()); err != nil {
				return wall.Probe{}, err
			}
			return wall.Probe{Driver: cached.Driver, Level: wall.GVisor, OK: true, CanPrepare: cached.CanPrepare, Version: cached.Version, Facts: cached.Facts}, nil
		}
	}
	result, err := CapabilityProbe(probeContext, "")
	err = errors.Join(err, finish())
	if err != nil {
		return wall.Probe{}, err
	}
	for _, probe := range result.Walls {
		if probe.Driver == "gvisor" {
			return wall.Probe{Driver: probe.Driver, Level: wall.GVisor, OK: probe.OK, CanPrepare: probe.CanPrepare, Version: probe.Version, Reason: probe.Reason, Fix: probe.Fix, Facts: probe.Facts}, nil
		}
	}
	return wall.Probe{}, UserError("wall_unavailable", "the native probe omitted its actual registered gvisor implementation", "Use one current helper and original runtime selection for this start.")
}

func cachedGvisorMatchesV1(probe api.WallProbe, runtimeKey, physicalKey string) bool {
	return runtimeKey != "" && physicalKey != "" && probe.Facts["native_closure_v1"] == physicalKey && probe.Driver == "gvisor" && probe.Level == "gvisor" && probe.OK && probe.Version == gvisor.NativeBuildVersionV1 && probe.Facts["runtime_manifest"] == runtimeKey
}
