//go:build linux

package host

import (
	"context"
	"os"
)

// Only closed host driver factories install this projection. The original
// accepted startup19 Source, not Spec data or a context flag, owns its roles.
func withOriginalCheckpointReservationV2(ctx context.Context, consume func([]*os.File) error) (bool, error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil || consume == nil {
		return false, vmNeedError("the reserved slot consumer has no original native boot receipt")
	}
	return receipt.WithOriginalCheckpointReservationV2(ctx, consume)
}

func hasOriginalCheckpointReservationV2(ctx context.Context) (bool, error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil {
		return false, vmNeedError("reserved mode selection has no actual original Boot receipt")
	}
	return receipt.HasOriginalCheckpointReservationV2(ctx)
}
