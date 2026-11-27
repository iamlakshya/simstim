//go:build linux

package microvm

import (
	"context"
	"fmt"
)

// PowerButton is the supervisor's bounded fallback when guest ctl stop fails.
func (s *sandbox) PowerButton(ctx context.Context) error {
	if s.driver.kind == nitroBackend {
		return fmt.Errorf("use the guest control stream to stop a Nitro Sandbox")
	}
	s.mu.Lock()
	control := s.control
	s.mu.Unlock()
	if control == nil {
		return fmt.Errorf("the VMM control channel is not running")
	}
	_, err := control.call(ctx, "power-button")
	return err
}
