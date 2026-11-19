package host

import (
	"context"
	"reflect"

	"github.com/iamlakshya/simstim/internal/wall"
)

// checkOriginalSavedImageNeedV1 binds public saved-root selection DATA to the
// actual original boot's admitted host domain. The native image store still
// opens and checks the SAME root descriptor/receipt/full hash/verity under its
// retained lease; this check cannot manufacture a root or execution grant.
func checkOriginalSavedImageNeedV1(ctx context.Context, spec wall.Spec) error {
	if spec.VM == nil || spec.VM.Image.SavedHost == nil {
		return nil
	}
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil || receipt.need == nil || receipt.spec.VM == nil || !reflect.DeepEqual(receipt.spec.VM.Image, spec.VM.Image) || !reflect.DeepEqual(receipt.need.Image, spec.VM.Image) {
		return vmNeedError("the saved image has no matching original native boot selection")
	}
	if err := spec.VM.Image.Validate(); err != nil {
		return err
	}
	domain, err := OriginalBootDomain(ctx)
	if err != nil {
		return err
	}
	if spec.VM.Image.SavedHost.HostID != domain.HostID {
		return vmNeedError("the original saved image belongs to another host domain")
	}
	return nil
}
