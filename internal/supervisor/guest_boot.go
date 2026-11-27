package supervisor

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

func (s *Supervisor) certifyGuestBoot(ctx context.Context, terminalABI string) error {
	if s.vmPlan == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.settleKills()
	unsafe := s.stopIntent || s.lost != "" || s.silent || s.rec.oom || s.rec.oomAgent || s.captureFailed
	s.mu.Unlock()
	if unsafe || s.link != nil && errors.Is(s.link.Err(), link.ErrBreach) {
		return execRefusal("the VM stopped, breached its link or hit OOM before boot certification")
	}
	if s.link != nil && s.link.Err() != nil {
		clean, ok := s.sb.(interface{ CleanExit() bool })
		if !ok || !clean.CleanExit() || !s.pendingEmpty() {
			return execRefusal("an unexpected VM link end cannot certify its boot")
		}
	}
	certifier, ok := s.sb.(wall.GuestBootCertifierV1)
	if !ok {
		return execRefusal("the VM driver has no prepared-guest boot certification contract")
	}
	// vmPlan exists only after the actual VMPrepare receipt matched v2.
	// The caller also checked an actual primary/selected-child terminal ACK.
	if err := certifier.CertifyGuestBootV1(ctx, proto.GuestOwnedPTYV2, terminalABI); err != nil {
		return wallError("the prepared VM boot could not be certified", err)
	}
	return nil
}

// CertifyGuestBootV1 preserves the optional driver contract through the
// ordinary runtime adapter, whose embedded interface otherwise hides it.
func (r vmRuntime) CertifyGuestBootV1(ctx context.Context, preparedABI, terminalABI string) error {
	certifier, ok := r.Sandbox.(wall.GuestBootCertifierV1)
	if !ok {
		return execRefusal("the owned VM runtime has no guest boot certifier")
	}
	return certifier.CertifyGuestBootV1(ctx, preparedABI, terminalABI)
}
