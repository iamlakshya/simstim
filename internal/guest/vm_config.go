package guest

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmconfig"
)

type vmPreparePlan = vmconfig.Plan

func validateVMPrepare(a proto.CtlVMPrepareArgs) (vmPreparePlan, error) { return vmconfig.Validate(a) }
func vmCaptureNonce(nonce string) bool                                  { return vmconfig.CaptureNonce(nonce) }
func vmError(message string) error {
	return &proto.Error{Code: "wall_init", Message: message, Hint: "Check the image and protected VM configuration, then resume the sandbox."}
}
func vmFilesystemError(operation string, err error) error {
	return fmt.Errorf("%s: %w", operation, err)
}
