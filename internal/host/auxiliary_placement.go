package host

import (
	"context"
	"os"
)

// withAuxiliaryPlacementV1 admits only the original private purge owner when
// drained. The concrete predicate checks that owner's actual control record,
// source and native counterpart under this SAME lock. Ordinary callers retain
// the normal drain refusal; a kind, token or manifest grants no exception.
func withAuxiliaryPlacementV1(ctx context.Context, change func(*placementControl) error, write bool) error {
	return withAuxiliaryPlacementRootV1(ctx, func(control *placementControl, _ *os.Root) error { return change(control) }, write)
}

func withAuxiliaryPlacementRootV1(ctx context.Context, change func(*placementControl, *os.Root) error, write bool) error {
	return withPlacementRoot(ctx, true, func(control *placementControl, guard *os.Root) error {
		if err := checkPurgeAuxiliaryContextControlV1(ctx, control); err != nil {
			return err
		}
		return change(control, guard)
	}, write)
}

// The original OP owns cancellation until physical cleanup, while the caller
// keeps its private original purge context. This changes no owner lifetime.
type auxiliaryLifetimeContextV1 struct {
	context.Context
	values context.Context
}

func (ctx auxiliaryLifetimeContextV1) Value(key any) any { return ctx.values.Value(key) }
