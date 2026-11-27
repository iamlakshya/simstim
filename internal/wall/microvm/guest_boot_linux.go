//go:build linux

package microvm

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

var _ wall.GuestBootCertifierV1 = (*sandbox)(nil)

// CertifyGuestBootV1 consumes the canonical supervisor's actual prepared/PTY
// result for this live owned VM. The expected runtime remains client-authorised
// s.spec; no guest ABI, inventory, layout or byte-only record supplies pins.
func (s *sandbox) CertifyGuestBootV1(ctx context.Context, preparedABI, terminalABI string) error {
	if preparedABI != proto.GuestOwnedPTYV2 || terminalABI != proto.GuestOwnedPTYV2 {
		return fmt.Errorf("microvm boot certification requires actual prepared and owned PTY v2")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	select {
	case <-s.done:
		if !s.cleanExitLocked() {
			s.mu.Unlock()
			return fmt.Errorf("the owned microvm ended without clean natural boot evidence")
		}
	default:
	}
	if !s.started || !s.helloChecked || s.forced || s.destroyed || s.destroying || s.command == nil || s.control == nil || s.spec.VM == nil {
		s.mu.Unlock()
		return fmt.Errorf("the same owned verified microvm is not live for guest certification")
	}
	owner, facts, runtime := s.owner, s.bootFacts, s.spec.VM.Runtime
	s.mu.Unlock()
	if err := owner.Err(); err != nil {
		return err
	}
	recording, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(owner, cancel)
	defer stop()
	facts, err := s.slotCertificationFactsV1(recording, facts)
	if err != nil {
		return err
	}
	if err := s.driver.recordBoot(recording, runtime, facts); err != nil {
		return fmt.Errorf("record the prepared owned microvm boot: %w", err)
	}
	return nil
}
