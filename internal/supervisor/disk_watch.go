package supervisor

import (
	"context"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// diskOptions is built by the sole runtime owner from actual factory-owned
// writable storage. Guest mount targets, rows and source paths cannot add roots.
// The owner pins roots before calling and closes them only after this loop joins.
type diskOptions struct {
	volume  *os.Root
	roots   []*os.Root
	process bool
	limit   int64
	ticks   <-chan time.Time
	measure func(context.Context, ...*os.Root) (files.Usage, error)
	space   func(*os.Root) (files.Space, error)
	// requestStop records an honest disk reason/alert and requests the SAME
	// owned stop. It must not wait for this loop or close its borrowed roots.
	requestStop func(error)
}

func diskFailure(code, message string) error {
	return &proto.Error{Code: code, Message: message, Hint: "Free space or correct sandbox.disk_max before waking the sandbox again."}
}

func checkDisk(ctx context.Context, options diskOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.volume == nil || options.limit < 0 {
		return diskFailure("disk_measure", "the sandbox disk budget has no valid owned filesystem")
	}
	space := options.space
	if space == nil {
		space = files.SpaceAt
	}
	free, err := space(options.volume)
	if err != nil || free.Total == 0 {
		return diskFailure("disk_measure", "the session filesystem's free space could not be measured")
	}
	if free.BelowFivePercent() {
		return diskFailure("disk_full", "the session filesystem has less than five percent free")
	}
	if !options.process || options.limit == 0 {
		return ctx.Err()
	}
	measure := options.measure
	if measure == nil {
		measure = files.MeasureUsage
	}
	usage, err := measure(ctx, options.roots...)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return diskFailure("disk_measure", "the process sandbox's allocated disk usage could not be measured")
	}
	if usage.Allocated > options.limit {
		return diskFailure("disk_full", "the process sandbox's allocated disk usage exceeds sandbox.disk_max")
	}
	return nil
}

// watchDisk is the one supervisor-owned minute disk loop. The actual runtime
// owner starts it before long input work, includes it in its join, and routes
// requestStop through the current-run lifecycle. It never destroys a workspace,
// publishes state, reads file data or interprets a guest heartbeat as room.
func watchDisk(ctx context.Context, options diskOptions) error {
	if options.requestStop == nil {
		return diskFailure("disk_measure", "the sandbox has no owned disk stop handler")
	}
	ticks := options.ticks
	if ticks == nil {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ticks:
			if !ok {
				err := diskFailure("disk_measure", "the disk measurement clock stopped")
				if ctx.Err() == nil {
					options.requestStop(err)
				}
				return err
			}
		}
		if err := checkDisk(ctx, options); err != nil {
			if ctx.Err() == nil {
				options.requestStop(err)
			}
			return err
		}
	}
}
