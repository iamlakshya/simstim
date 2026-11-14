package files

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Space is available and total space of one pinned directory's filesystem.
type Space struct {
	Available uint64
	Total     uint64
	// AllocationUnit comes from this SAME pinned filesystem query.
	// Zero means geometry was not supplied, never a guessed page size.
	AllocationUnit uint64
}

// BelowFivePercent uses an exact integer comparison. Exactly five percent
// remains allowed. No multiplication can overflow for a very large volume.
func (s Space) BelowFivePercent() bool {
	threshold := s.Total / 20
	if s.Total%20 != 0 {
		threshold++
	}
	return s.Available < threshold
}

// CheckSpace is a creation guard, not a quota or a substitute for the actual
// supervisor's minute disk watch. Read failures refuse rather than claim room.
func CheckSpace(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	space, err := SpaceAt(root)
	if err != nil || space.Total == 0 {
		return &proto.Error{Code: "disk_measure", Message: "the destination filesystem's free space could not be checked",
			Hint: "Check the session filesystem before copying or starting it again."}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if space.BelowFivePercent() {
		return &proto.Error{Code: "disk_full", Message: "the destination filesystem has less than five percent free",
			Hint: "Free some space before copying or starting the sandbox again."}
	}
	return nil
}
