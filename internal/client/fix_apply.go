package client

import (
	"context"
	"fmt"
)

// StoppedFixesError records what completed before cancellation stopped a
// repair. It retains the context error for errors.Is.
type StoppedFixesError struct {
	Done []string
	Err  error
}

func (e *StoppedFixesError) Error() string { return "apply the fixes: " + e.Err.Error() }
func (e *StoppedFixesError) Unwrap() error { return e.Err }

// RemovedFile is the accepted repair progress hook. Only the context-owned
// reporter calls it after an actual successful removal.
var RemovedFile = func(string) {}

// ApplyLocalFixes retains the canonical ownership checks and returns the
// completed work when cancellation interrupts a later file or repair.
func ApplyLocalFixes(ctx context.Context, ids []string) ([]Fix, error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	// The guarded probe distinguishes cancellation from a dead socket.
	all := append(sshConfigFix(), controlSocketFix(ctx)...)
	out := make([]Fix, 0, len(all))
	var stopped error
	var done []string
	for _, f := range all {
		if want[f.ID] && stopped == nil {
			stopped = ctx.Err()
		}
		if want[f.ID] && stopped == nil {
			run := withLocalRemovalReport(ctx, func(path string) {
				f.Changed = append(f.Changed, "removed "+path)
				RemovedFile(path)
			})
			if err := f.run(run); err != nil {
				f.Error = err.Error()
			} else {
				f.Done = true
			}
			switch {
			case f.Done:
				done = append(done, "fixed: "+f.Problem)
			case len(f.Changed) > 0:
				done = append(done, fmt.Sprintf("removed %d of %d %s", len(f.Changed), f.files, f.what))
			}
		}
		out = append(out, f.Fix)
	}
	if stopped == nil {
		stopped = ctx.Err()
	}
	if stopped != nil {
		return out, &StoppedFixesError{Done: done, Err: stopped}
	}
	return out, nil
}
