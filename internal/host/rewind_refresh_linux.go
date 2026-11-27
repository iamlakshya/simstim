//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/checkpoint"
)

func (p *bridgeRewindPreparationV1) refreshRewindColdArtifactsV1(ctx context.Context) error {
	if p.artifactsRefreshed {
		return nil
	}
	if p.cold == nil || p.stopped == nil {
		return rewindFailureV1("the disk refresh has no actual cold and stopped owners")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	if p.slotSnapshot != nil {
		factory, ok := p.slotSnapshot.(rewindSlotColdV1)
		if !ok {
			return rewindFailureV1("the actual slot factory cannot refresh the committed disk")
		}
		if err := factory.RefreshStoppedColdV1(ctx, p.operation, p.stopped, p.cold, p.disk); err != nil {
			return err
		}
		p.artifactsRefreshed = true
		return nil
	}
	cold := p.cold
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if cold.closed || cold.bound || cold.handed || cold.operation != p.operation || cold.vacancy == nil || cold.vacancy.stopped != p.stopped || cold.artifacts == nil {
		return rewindFailureV1("the actual stopped cold source changed before disk refresh")
	}
	if err := cold.vacancy.Check(ctx); err != nil {
		return err
	}
	next, err := captureAuxiliaryArtifactsV1(ctx, cold.vacancy)
	if err != nil {
		return err
	}
	if p.disk != nil && !p.rolledBack {
		physical := p.disk.Journal()
		if physical.Phase != checkpoint.RestoreFinalizedV1 || physical.Replacement.Name != "disk.img" {
			return errors.Join(rewindFailureV1("the disk replacement has not physically finalized"), next.Close())
		}
		if p.storage == nil || p.storage.vm == nil {
			return errors.Join(rewindFailureV1("the replacement check lost its actual retained private VM root"), next.Close())
		}
		if err := physical.Replacement.Reference.CheckFile(p.storage.vm, "disk.img", next.disk, 0600); err != nil {
			return errors.Join(err, next.Close())
		}
	}
	old := cold.artifacts
	cold.artifacts = next
	if err := old.Close(); err != nil {
		return err
	}
	p.artifactsRefreshed = true
	return cold.checkLockedV1(ctx, p.operation)
}
