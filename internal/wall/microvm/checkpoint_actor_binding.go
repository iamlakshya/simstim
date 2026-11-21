package microvm

import (
	"context"
	"fmt"
	"net"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

type nativeSubordinateCheckpointV3 interface {
	hasSubordinateCheckpointV3() bool
	withSubordinateCheckpointV3(context.Context, net.Conn, vmneed.NeedV1, func(checkpoint.BackendV1) error) error
}

// HasSubordinateCheckpointBackendV3 is routing data only. The actual borrow
// separately checks this SAME engine, complete Need and original native B.
func (binding *NativeBindingV1) HasSubordinateCheckpointBackendV3() bool {
	if binding == nil || binding.verifier == nil {
		return false
	}
	owner, ok := binding.verifier.(nativeSubordinateCheckpointV3)
	return ok && owner.hasSubordinateCheckpointV3()
}

// WithSubordinateCheckpointBackendV3 lends only this actual source's fixed
// running storage operations. The backend cannot escape the callback. Code,
// an O_PATH directory or a copied expected Need cannot issue this binding.
func (binding *NativeBindingV1) WithSubordinateCheckpointBackendV3(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, use func(checkpoint.BackendV1) error) error {
	if binding == nil || binding.verifier == nil || ctx == nil || use == nil {
		return fmt.Errorf("the actor checkpoint has no actual original native binding")
	}
	owner, ok := binding.verifier.(nativeSubordinateCheckpointV3)
	if !ok || !owner.hasSubordinateCheckpointV3() {
		return fmt.Errorf("the original runtime has no subordinate checkpoint actor")
	}
	return owner.withSubordinateCheckpointV3(ctx, connection, expected, use)
}
