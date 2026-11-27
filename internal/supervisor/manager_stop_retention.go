package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/host"
)

// retainManagerCustodyForSameRequestStopV1 is reached only after the actual
// accepted native stop/rewind scope checks and before its intent and ACK.
func (s *Supervisor) retainManagerCustodyForSameRequestStopV1(ctx context.Context, owner *host.OwnedSupervisorLease) error {
	root := s.owner()
	root.mu.Lock()
	valid := owner != nil && root.bootOwner == owner && root.launched && root.runtimeCleaned != nil && !channelClosed(root.runtimeCleaned) && root.generation != ""
	root.mu.Unlock()
	if !valid || !owner.Valid(root.id) || !owner.StartupCommittedV1() {
		return execRefusal("the retained stop has no actual original committed supervisor")
	}
	return owner.RetainManagerStopForSameRequestV1(ctx)
}
