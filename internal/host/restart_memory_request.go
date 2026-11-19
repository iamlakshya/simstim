package host

import (
	"maps"
	"reflect"
	"runtime"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

type restartSizeRequestV1 struct {
	args api.ResumeArgs
}

func captureRestartSizeRequestV1(a api.ResumeArgs) *restartSizeRequestV1 {
	return &restartSizeRequestV1{args: api.ResumeArgs{Layers: api.CloneLayers(a.Layers), GrantHash: a.GrantHash, GrantAnswers: maps.Clone(a.GrantAnswers)}}
}

func restartSizePreviewV1(current *Manifest, fresh []api.Layer, request *restartSizeRequestV1) (Manifest, error) {
	preview := CloneManifest(*current)
	if request != nil {
		fresh = request.args.Layers
	}
	if err := ApplyResumeLayers(&preview, api.CloneLayers(fresh)); err != nil {
		return Manifest{}, err
	}
	if request != nil {
		if err := ApplyRepositoryResume(&preview, request.args); err != nil {
			return Manifest{}, err
		}
	}
	return preview, nil
}

func restartLimitsForRequestV1(current, preview *Manifest, requested int64, restore bool) (Limits, error) {
	after := current.Limits
	if !isWalledMemoryV1(current) {
		if requested == api.MemoryAuto {
			after.MemoryAsked = 0
		}
		if requested > 0 {
			after.MemoryAsked = requested
		}
		after.MemoryMax, after.Declared = sessionMemoryMax(after.MemoryAsked), 0
		return after, nil
	}
	var beforeValues, values api.SandboxValues
	if current.Policy != nil {
		beforeValues = current.Policy.Sandbox
	}
	if preview.Policy != nil {
		values = preview.Policy.Sandbox
	}
	changedSize := !reflect.DeepEqual(beforeValues.Memory, values.Memory)
	if requested > 0 {
		after.MemoryMax, after.MemoryAsked = requested, requested
	} else if !restore && (requested == api.MemoryAuto || changedSize) {
		agent, _ := agents.Find(current.Agent.Kind)
		size, err := resolvedWalledSizeV1(values, agent)
		if err != nil {
			return Limits{}, err
		}
		after.MemoryMax, after.MemoryAsked = size.Memory, 0
		if len(current.VMNeedV1) != 0 {
			budget, err := vmneed.BudgetFor(size.Memory)
			if err != nil {
				return Limits{}, err
			}
			after.MemoryMax = budget.Limit
		}
	}
	after.Declared = after.MemoryMax
	if err := checkExactWalledCapV1(after.MemoryMax); err != nil {
		return Limits{}, err
	}
	if len(current.VMNeedV1) != 0 {
		need, err := vmneed.DecodeV1(current.VMNeedV1, runtime.GOARCH)
		if err != nil {
			return Limits{}, err
		}
		budget, err := vmneed.BudgetFor(need.GuestRAM)
		if err != nil || after.MemoryMax != current.Limits.MemoryMax || after.MemoryMax != budget.Limit {
			return Limits{}, vmNeedError("a changed VM size needs a fresh authenticated client Need before restart")
		}
	}
	return after, nil
}
