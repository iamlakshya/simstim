//go:build linux

package microvm

import (
	"context"
	"fmt"
	"net"

	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

// OriginalAccountModeV1 observes the concrete engine's original native start.
// Its result is display data only, never a source, boot or admission grant.
func OriginalAccountModeV1(ctx context.Context, runtime wall.Sandbox, connection net.Conn, binding *NativeBindingV1, expected vmneed.NeedV1) (bool, error) {
	s, ok := runtime.(*sandbox)
	if ctx == nil {
		return false, fmt.Errorf("the account observation has no original owner context")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !ok || s == nil || connection == nil || binding == nil {
		return false, fmt.Errorf("the account observation has no original concrete VM and connection")
	}
	// Check dispatches to the actual private verifier. The slot verifier
	// calls slotNativeProof.check on the same complete original Need.
	if err := binding.Check(ctx, connection, expected); err != nil {
		return false, err
	}
	switch original := binding.verifier.(type) {
	case *slotSandboxBinding:
		if original.sandbox != s || original.connection != connection || original.proof == nil {
			return false, fmt.Errorf("the account observation belongs to another native slot start")
		}
		s.mu.Lock()
		selected := s.uid == slotUIDModeV1Name && s.control != nil && s.control.native == original.proof
		s.mu.Unlock()
		if !selected {
			return false, fmt.Errorf("the native slot issuer differs from the selected VM")
		}
		return true, nil
	case *sandboxNativeBinding:
		if original.sandbox != s || original.connection != connection {
			return false, fmt.Errorf("the account observation belongs to another legacy VM start")
		}
		return false, nil
	default:
		return false, fmt.Errorf("the account observation has no original native VM issuer")
	}
}
