package supervisor

import (
	"context"
	"errors"
	"sync"

	"github.com/iamlakshya/simstim/internal/host"
)

// bootAgentsCustodyV1 owns only actual returned factory objects, including a
// nonnil source on error. It issues no inventory, selection, boot or operation
// authority. A failed Close retains the SAME object for the next cleanup.
type bootAgentsCustodyV1 struct {
	mu       sync.Mutex
	receipts map[*host.InstalledAgentsReceiptV1]struct{}
}

func (s *Supervisor) prepareOwnedBootAgentsV1(ctx context.Context) error {
	if err := s.bootAgentsCustody.closeV1(); err != nil {
		return err
	}
	receipt, err := s.prepareBootAgents(ctx)
	if receipt != nil {
		owner := &s.bootAgentsCustody
		owner.mu.Lock()
		if owner.receipts == nil {
			owner.receipts = map[*host.InstalledAgentsReceiptV1]struct{}{}
		}
		owner.receipts[receipt] = struct{}{}
		owner.mu.Unlock()
	}
	return err
}

func (owner *bootAgentsCustodyV1) closeV1() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	var result error
	for receipt := range owner.receipts {
		if err := receipt.Close(); err != nil {
			result = errors.Join(result, err)
		} else {
			delete(owner.receipts, receipt)
		}
	}
	return result
}
