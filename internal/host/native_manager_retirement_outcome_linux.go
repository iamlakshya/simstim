//go:build linux

package host

import (
	"context"
	"errors"
)

// This concrete resource outcome keeps semantic errors separate from the
// SAME original manager's physical retirement. It grants no manager/unit
// admission. Callers retain/report diagnostic at their original operation
// boundary; only unknown governs native resource and charge retention.
func retireNativeManagerV4(ctx context.Context, manager *nativeUserManagerV1) (diagnostic, unknown error) {
	if manager == nil {
		return nil, nil
	}
	closed := manager.Close()
	// Request cancellation is an operation diagnostic, not evidence that
	// these SAME first-close native facts became unknown after retirement.
	unknown = manager.checkNativeRetiredV3(context.Background())
	if ctx == nil {
		diagnostic = sessionLeaseError("the manager cleanup has no original caller context")
	} else {
		diagnostic = ctx.Err()
	}
	diagnostic = errors.Join(diagnostic, manager.CloseDiagnosticsV3())
	if unknown != nil {
		return diagnostic, errors.Join(closed, unknown)
	}
	return errors.Join(closed, diagnostic), nil
}
