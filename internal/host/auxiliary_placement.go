package host

import "context"

// withAuxiliaryPlacementV1 admits only the original private purge owner when
// drained. The concrete predicate checks that owner's actual control record,
// source and native counterpart under this SAME lock. Ordinary callers retain
// the normal drain refusal; a kind, token or manifest grants no exception.
func withAuxiliaryPlacementV1(ctx context.Context, change func(*placementControl) error, write bool) error {
	return withPlacement(ctx, true, func(control *placementControl) error {
		if err := checkPurgeAuxiliaryContextControlV1(ctx, control); err != nil {
			return err
		}
		return change(control)
	}, write)
}

// The original OP owns cancellation until physical cleanup, while the caller
// keeps its private original purge context. This changes no owner lifetime.
type auxiliaryLifetimeContextV1 struct {
	context.Context
	values context.Context
}

func (ctx auxiliaryLifetimeContextV1) Value(key any) any { return ctx.values.Value(key) }
