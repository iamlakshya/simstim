package guest

import (
	"context"
	"math"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

// checkInputStageSpace checks the actual pinned stage filesystem before the
// receiver launches. SealedBytes is separate memory work, not filesystem room
// or an RSS claim; native memory admission remains with the runtime owner.
func checkInputStageSpace(ctx context.Context, stage *os.Root, summary api.InputSummary) (inputtransfer.StagingFootprint, error) {
	if err := ctx.Err(); err != nil {
		return inputtransfer.StagingFootprint{}, err
	}
	space, err := files.SpaceAt(stage)
	if err != nil || space.Total == 0 || space.AllocationUnit == 0 || space.AllocationUnit > math.MaxInt64 {
		return inputtransfer.StagingFootprint{}, inputError("the pinned input staging capacity or allocation geometry is unavailable")
	}
	plan, err := inputtransfer.PlanStaging(summary, int64(space.AllocationUnit), int64(os.Getpagesize()))
	if err != nil {
		return plan, err
	}
	if err = ctx.Err(); err != nil {
		return plan, err
	}
	if uint64(plan.DiskBytes) > space.Available || (files.Space{Available: space.Available - uint64(plan.DiskBytes), Total: space.Total}).BelowFivePercent() {
		return plan, &proto.Error{Code: "disk_full", Message: "the declared input staging work does not fit in the current filesystem capacity", Hint: "Free space or increase the sandbox's backing storage before retrying the same declared input limits."}
	}
	return plan, nil
}
