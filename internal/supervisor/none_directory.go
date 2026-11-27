package supervisor

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

// Publish while the actual original borrowed OP/creator/holder remain held,
// before any user command. Booting DATA makes no completed-cleanup claim.
func (s *Supervisor) publishNoneDirectoryCreationV1(ctx context.Context, receipt *host.OriginalVMNeed, birth *none.LiveOwnerV1) error {
	if !s.unwalledV1() || receipt == nil || receipt != host.OriginalVMNeedFromContext(s.lifetimeContext()) || birth == nil || s.noneLive != birth || s.bootOwner == nil || !s.bootOwner.Valid(s.id) || s.bootOwner.Generation() != s.generation {
		return execRefusal("the original None directory publisher has another actual creator")
	}
	raw, err := receipt.NoneDirectoryCreationDataV1(ctx, birth)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.rec.phase != host.LifeBooting || s.launched {
		s.mu.Unlock()
		return execRefusal("the original None directory was published outside its real preparation")
	}
	s.m.NoneDirectoryCreation = append([]byte(nil), raw...)
	s.m.Run.Generation = s.generation
	s.m.Run.BootID = host.BootID()
	s.m.Run.HolderPID = os.Getpid()
	snapshot := s.snapshot()
	s.mu.Unlock()
	return s.save(&snapshot, false)
}
