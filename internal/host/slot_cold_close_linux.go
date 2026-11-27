//go:build linux

package host

import (
	"context"
	"errors"
	"os"
)

func (slot *slotColdArtifactsV2) closeV2(artifacts *auxiliaryArtifactsV1) error {
	if slot == nil || slot.issuer != slot || slot.owner == nil || slot.artifacts != artifacts || artifacts == nil || artifacts.slot != slot {
		return rewindFailureV1("a copied slot artifact delegate cannot release original custody")
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.closed || slot.closeErr != nil {
		return slot.closeErr
	}
	if slot.dispatched != nil {
		if slot.retireTo != nil {
			retry := slot.owner.slotRetry
			next := slot.retireTo
			if retry == nil || retry.issuer != retry || retry.origin != slot.owner || retry.oldArtifacts != artifacts || retry.next != next.cold || retry.receipt == nil || retry.receipt.previousColdV1() != slot.cold || next.issuer != next || next.owner != slot.owner || slot.owner.cold != next.cold || len(next.roles) != 18 {
				return rewindFailureV1("the retired slot artifacts have no actual failed-creation successor")
			}
			if err := errors.Join(retry.receipt.checkV1(context.Background(), slot.owner.operation), checkSameSlotLeaseOFDV2(slot.roles[17], next.roles[17])); err != nil {
				return err
			}
		} else {
			attempt := slot.owner.takeover
			if attempt == nil || attempt.cold != slot.cold || attempt.launch != slot.dispatched || !attempt.verified {
				return rewindFailureV1("the original reserved lease needs the actual new Source handoff before release")
			}
			if err := attempt.checkV2(context.Background()); err != nil {
				return err
			}
		}
	}

	slot.closing = true
	if err := artifacts.closeFilesV2(); err != nil {
		slot.closeErr = err
		return err
	}
	for _, file := range append(append([]*os.File(nil), slot.roles...), slot.supervisor) {
		if file != nil {
			slot.closeErr = errors.Join(slot.closeErr, file.Close())
		}
	}
	if slot.closeErr != nil {
		return slot.closeErr
	}
	// The old outside reservation copies are gone while this actual new
	// Source still retains its independently observed SAME OFD.
	if slot.dispatched != nil && slot.retireTo == nil {
		slot.closeErr = errors.Join(slot.closeErr, slot.owner.takeover.closeOwnedV2())
	}
	slot.closed = slot.closeErr == nil
	return slot.closeErr
}

func (attempt *slotColdTakeoverV2) closeOwnedV2() error {
	if attempt == nil {
		return nil
	}
	if attempt.issuer != attempt {
		return rewindFailureV1("a copied slot takeover cannot release original native owners")
	}
	if attempt.closed {
		return attempt.closeErr
	}
	// This is resource cleanup after the actual same-OFD observation, never a
	// constructor or a new permission from a cached ready flag.
	if attempt.warm != nil {
		if err := attempt.warm.Close(); err != nil {
			return err
		}
	}
	if attempt.source != nil {
		if err := attempt.source.Close(); err != nil {
			return err
		}
	}
	if attempt.unexpected != nil {
		if err := attempt.unexpected.Close(); err != nil {
			return err
		}
	}
	if attempt.creation != nil {
		attempt.closeErr = errors.Join(attempt.closeErr, attempt.creation.Close())
	}
	attempt.closed = attempt.closeErr == nil
	return attempt.closeErr
}

// The caller holds Cold.life and Cold.mu. Refused slot cleanup keeps the
// same objects/lifetime callable instead of setting closed before an error.
func (cold *ColdResumeV1) closeSlotArtifactsV2() error {
	if err := cold.artifacts.Close(); err != nil {
		return err
	}
	if err := cold.vacancy.Close(); err != nil {
		return err
	}
	if cold.creation != nil {
		if err := cold.creation.Close(); err != nil {
			cold.closeErr = errors.Join(cold.closeErr, err)
			return cold.closeErr
		}
		cold.creation = nil
	}
	if cold.rewindCreationGroup != nil {
		if err := cold.rewindCreationGroup.Close(); err != nil {
			cold.closeErr = errors.Join(cold.closeErr, err)
			return cold.closeErr
		}
		cold.rewindCreationGroup = nil
	}
	cold.closed = cold.closeErr == nil
	return cold.closeErr
}
