//go:build linux

package microvm

import "errors"

func (binding *slotSandboxBinding) closeOrKeepSlotOwnerV1(owner interface{ Close() error }) error {
	if owner == nil {
		return nil
	}
	if err := owner.Close(); err != nil {
		binding.sandbox.mu.Lock()
		binding.sandbox.partialSlotOwners = append(binding.sandbox.partialSlotOwners, owner)
		binding.sandbox.mu.Unlock()
		return err
	}
	return nil
}

func (s *sandbox) closePartialSlotOwnersV1() error {
	s.mu.Lock()
	owners := append([]interface{ Close() error }(nil), s.partialSlotOwners...)
	s.partialSlotOwners = nil
	s.mu.Unlock()
	var failure error
	var retained []interface{ Close() error }
	for _, owner := range owners {
		if err := owner.Close(); err != nil {
			failure = errors.Join(failure, err)
			retained = append(retained, owner)
		}
	}
	if len(retained) > 0 {
		s.mu.Lock()
		s.partialSlotOwners = append(s.partialSlotOwners, retained...)
		s.mu.Unlock()
	}
	return failure
}
