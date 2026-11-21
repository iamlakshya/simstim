//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/checkpoint"
)

// rewindSlotColdV1 is implemented only by H15's actual outside slot factory.
// The host never fills private-root artifacts from a slot path or a DataRef.
type rewindSlotColdV1 interface {
	CaptureStoppedColdV1(context.Context, *SessionOperation, *StoppedSessionV1) (*ColdResumeV1, error)
	RefreshStoppedColdV1(context.Context, *SessionOperation, *StoppedSessionV1, *ColdResumeV1, checkpoint.PreparedRestoreV1) error
}

type rewindSlotNewSourceV2 interface {
	ValidateNewColdSourceV2(context.Context, *SessionOperation, *ColdResumeV1) error
}

func (p *bridgeRewindPreparationV1) performRewindV1() (_ *api.RewindResult, returned error) {
	if err := p.checkCommitEnvelopeV1(p.execution, p.fresh); err != nil {
		return nil, err
	}
	if p.phase.Load() == rewindCompletedV1 {
		if p.journal != nil && !p.journal.removalDoneV1() {
			if !p.journal.removed {
				if err := p.journal.phaseV1(p.execution, "completed"); err != nil {
					return nil, err
				}
			}
			if err := p.journal.removeV1(p.execution); err != nil {
				return nil, err
			}
		}
		if p.rolledBack {
			return nil, context.Canceled
		}
		return p.result, nil
	}
	if p.phase.Load() == rewindMetadataCommittedV1 || p.phase.Load() == rewindRolledBackV1 || p.phase.Load() == rewindRestartingV1 {
		if err := p.finishRewindRestartV1(p.execution); err != nil {
			return nil, err
		}
		if p.rolledBack {
			return nil, context.Canceled
		}
		return p.result, nil
	}
	if p.phase.Load() == rewindStoppingV1 && p.stopped == nil {
		stopped, err := p.warm.settleOriginalRewindStopV1(p.execution, p)
		if stopped == nil {
			return nil, err
		}
		p.stopped = stopped
		p.phase.Store(rewindStoppedV1)
	}
	if p.phase.Load() == rewindPreparedV1 {
		if err := p.work.Err(); err != nil {
			return nil, err
		}
		if err := p.checkSelectedSnapshotV1(p.work); err != nil {
			return nil, err
		}
		if _, err := p.previewStartV1(p.work, p.fresh, true); err != nil {
			return nil, err
		}
		p.phase.Store(rewindStoppingV1)
		stopped, err := p.warm.stopForRewindV1(p.work, p)
		if stopped != nil {
			p.stopped = stopped
			p.phase.Store(rewindStoppedV1)
		}
		if stopped == nil {
			return nil, err
		}
		if err != nil && p.bridge.log != nil {
			p.bridge.log.Warn("the original rewind stop was physically settled after a control error", "err", err)
		}
	}
	if p.stopped == nil {
		return nil, rewindFailureV1("the original rewind stop has not physically settled")
	}
	if err := p.stopped.Check(p.execution, p.operation); err != nil {
		return nil, err
	}
	if p.cold == nil {
		if err := p.captureColdForRewindV1(p.execution); err != nil {
			return nil, err
		}
	}
	if p.accounting == nil {
		if err := p.reserveRewindRestartV1(p.execution); err != nil {
			return nil, err
		}
	}
	if p.work.Err() != nil && p.phase.Load() < rewindMetadataCommittedV1 {
		if err := p.rollbackRewindV1(p.execution); err != nil {
			return nil, err
		}
		if err := p.finishRewindRestartV1(p.execution); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	if p.disk == nil {
		if err := p.openStoppedRewindStorageV1(p.execution); err != nil {
			return nil, err
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		generation := hex.EncodeToString(nonce[:])
		metadata, err := p.safetyMetadataV1(p.execution)
		if err != nil {
			return nil, err
		}
		input := checkpoint.TransactionV1{Version: 1, Generation: generation, Safety: checkpoint.Entry{Time: time.Now().UTC(), Metadata: metadata}}
		p.disk, err = p.backend.PrepareRestore(p.work, p.snapshot, input, func(ctx context.Context) error { return p.stopped.Check(ctx, p.operation) })
		if p.disk != nil {
			p.phase.Store(rewindDiskPreparedV1)
		}
		if err != nil {
			return nil, err
		}
	}
	if p.phase.Load() == rewindDiskPreparedV1 {
		if p.disk == nil || p.disk.Journal().Phase != checkpoint.RestorePreparedV1 {
			return nil, rewindFailureV1("the original disk preparation has not positively settled its physical roles")
		}
		generation := p.disk.Journal().Generation
		if p.metadataTxn != nil && !p.metadataTxn.prepared {
			if err := p.metadataTxn.closeV1(); err != nil {
				return nil, err
			}
			p.metadataTxn = nil
		}
		if p.metadataTxn == nil {
			var err error
			p.metadataTxn, err = prepareRewindMetadataV1(p.work, p, generation, p.snapshot.Entry().Metadata)
			if err != nil {
				return nil, err
			}
		}
		if p.journal == nil {
			var err error
			p.journal, err = newRewindJournalV1(p.execution, p, generation)
			if err != nil {
				return nil, err
			}
		}
		if err := p.work.Err(); err != nil {
			return nil, err
		}
		if err := p.journal.phaseV1(p.execution, "disk_exchanging"); err != nil {
			return nil, err
		}
		err := p.disk.Commit(p.work)
		if p.disk.Journal().Phase == checkpoint.RestoreExchangedV1 {
			p.phase.Store(rewindExchangedV1)
		}
		if err != nil {
			return nil, err
		}
		if err := p.journal.phaseV1(p.execution, "disk_exchanged"); err != nil {
			return nil, err
		}
	}
	if p.phase.Load() == rewindExchangedV1 {
		if err := p.metadataTxn.publishV1(p.work); err != nil {
			return nil, err
		}
		if err := p.journal.phaseV1(p.execution, "metadata_committed"); err != nil {
			return nil, err
		}
		if err := p.metadataTxn.markCommittedV1(p.execution); err != nil {
			return nil, err
		}
		p.phase.Store(rewindMetadataCommittedV1)
	}
	// The data commit is complete. Only its already-authorised fresh payload
	// can finish the native restart; a later cancel cannot stop that new run.
	if err := p.finishRewindRestartV1(p.execution); err != nil {
		return nil, err
	}
	return p.result, nil
}

func (p *bridgeRewindPreparationV1) captureColdForRewindV1(ctx context.Context) error {
	if p == nil || p.stopped == nil || p.operation == nil {
		return rewindFailureV1("the cold restart has no original stopped owner")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	if p.slotSnapshot != nil {
		factory, ok := p.slotSnapshot.(rewindSlotColdV1)
		if !ok {
			return rewindFailureV1("the actual slot factory has no stopped cold adapter")
		}
		cold, err := factory.CaptureStoppedColdV1(ctx, p.operation, p.stopped)
		if cold != nil {
			p.cold = cold
		}
		return err
	}
	return p.captureLocalColdForRewindV1(ctx)
}

func (p *bridgeRewindPreparationV1) reserveRewindRestartV1(ctx context.Context) error {
	if p.cold == nil {
		return rewindFailureV1("the restart reservation has no actual cold source")
	}
	if p.publication == nil {
		publication, err := beginRuntimePublicationKind(p.execution, "rewind_prepare", nil, p.operation)
		if err != nil {
			return err
		}
		p.publication = publication
	}
	p.operation.gate.Lock()
	admitted := p.operation.restartMemory != nil
	p.operation.gate.Unlock()
	if !admitted {
		if err := admitRestartMemoryRequestV1(ctx, p.operation, p.fresh); err != nil {
			return err
		}
	}
	preview, err := p.previewStartV1(ctx, p.fresh, true)
	if err != nil {
		return err
	}
	if err := p.cold.CheckManifest(ctx, p.operation, preview); err != nil {
		return err
	}
	lease, err := beginResumePreparationV1(ctx, p.operation, p.cold, preview, p.token)
	if lease != nil {
		p.accounting = lease
	}
	return err
}

func (p *bridgeRewindPreparationV1) safetyMetadataV1(ctx context.Context) (checkpoint.Metadata, error) {
	metadata := checkpoint.Metadata{AgentSessionIDs: map[string]string{}, Turn: "before rewind"}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return metadata, err
	}
	for _, member := range p.members {
		current, err := member.read(ctx, p.operation)
		if err != nil || current == nil {
			return metadata, errors.Join(snapshotChanged(), err)
		}
		if current.Agent.Kind != member.manifest.Agent.Kind {
			return metadata, rewindFailureV1("the stopped member changed its original agent")
		}
		metadata.AgentSessionIDs[member.id] = current.Agent.AgentSessionID
		if member.id == p.operation.Primary() && current.Guest != nil {
			for _, ref := range current.Guest.Refs {
				metadata.Refs = append(metadata.Refs, checkpoint.Ref{Name: ref.Name, SHA: ref.OID})
			}
		}
	}
	return metadata, nil
}

func (p *bridgeRewindPreparationV1) rollbackRewindV1(ctx context.Context) error {
	if p == nil || p.stopped == nil {
		return rewindFailureV1("rollback has no original stopped owner")
	}
	if p.phase.Load() >= rewindMetadataCommittedV1 && p.phase.Load() != rewindRolledBackV1 {
		return rewindFailureV1("the completed disk and conversation commit must finish its original restart")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	if p.disk != nil {
		if err := p.disk.Rollback(ctx); err != nil {
			return err
		}
	}
	if p.metadataTxn != nil {
		if err := p.metadataTxn.rollbackV1(ctx); err != nil {
			return err
		}
	}
	p.rolledBack = true
	p.phase.Store(rewindRolledBackV1)
	if p.journal != nil {
		if err := p.journal.phaseV1(ctx, "rolled_back"); err != nil {
			return err
		}
	}
	return nil
}

func (p *bridgeRewindPreparationV1) finishRewindRestartV1(ctx context.Context) error {
	if p.phase.Load() == rewindCompletedV1 {
		return nil
	}
	if p.phase.Load() != rewindMetadataCommittedV1 && p.phase.Load() != rewindRolledBackV1 && p.phase.Load() != rewindRestartingV1 {
		return rewindFailureV1("the original data transaction has not settled before restart")
	}
	if p.phase.Load() != rewindRestartingV1 {
		if p.disk != nil && !p.rolledBack {
			if err := p.disk.Finalize(ctx); err != nil {
				return err
			}
			if err := p.journal.phaseV1(ctx, "disk_finalized"); err != nil {
				return err
			}
		}
		if p.metadataTxn != nil {
			if err := p.metadataTxn.closeV1(); err != nil {
				return err
			}
		}
		if p.backend != nil && !p.rolledBack {
			if err := p.backend.Trim(ctx, p.keep); err != nil {
				return err
			}
		}
		if err := p.refreshRewindColdArtifactsV1(ctx); err != nil {
			return err
		}
		if p.disk != nil {
			if err := p.disk.Close(); err != nil {
				return err
			}
		}
		if p.storage != nil {
			if err := p.storage.closeV1(); err != nil {
				return err
			}
		}
		if p.journal != nil {
			if err := p.journal.phaseV1(ctx, "ready_to_restart"); err != nil {
				return err
			}
		}
		p.phase.Store(rewindRestartingV1)
	}
	if !p.artifactsRefreshed {
		return rewindFailureV1("the restart has no actual post-transaction disk artifact owner")
	}
	p.cold.mu.Lock()
	alreadyBound := p.cold.bound
	p.cold.mu.Unlock()
	if alreadyBound {
		if ready, err := p.reconcileReadyRewindV1(ctx); ready || err != nil {
			return err
		}
		if err := p.retryFailedRewindCreationV1(ctx); err != nil {
			return err
		}
		if p.accounting == nil {
			if err := p.reserveRewindRestartV1(ctx); err != nil {
				return err
			}
		}
	}
	result, err := p.cold.Resume(p.execution, p.operation, p.fresh)
	if err != nil {
		return err
	}
	if result == nil || result.Session.ID != p.selection.ID {
		return rewindFailureV1("the original cold restart returned another selected session")
	}
	if p.slotSnapshot != nil {
		actual, ok := p.slotSnapshot.(rewindSlotNewSourceV2)
		if !ok {
			return rewindFailureV1("the reserved slot has no actual new-source handoff verifier")
		}
		if err := actual.ValidateNewColdSourceV2(ctx, p.operation, p.cold); err != nil {
			return err
		}
	}
	if !p.rolledBack {
		p.result = &api.RewindResult{ID: p.selection.ID, Checkpoint: p.selection.Checkpoint, Session: result.Session}
	}
	p.phase.Store(rewindCompletedV1)
	if p.journal != nil {
		if err := p.journal.phaseV1(ctx, "completed"); err != nil {
			return err
		}
		if err := p.journal.removeV1(ctx); err != nil {
			return err
		}
	}
	return nil
}

// A late response/metadata failure after the actual tracked creation became
// ready must never turn cancellation into a stop of that new runtime. This
// reports only the already-owned outcome and creates no new Source.
func (p *bridgeRewindPreparationV1) reconcileReadyRewindV1(ctx context.Context) (bool, error) {
	operation := p.operation
	operation.gate.Lock()
	attempt := operation.startup
	operation.gate.Unlock()
	if attempt == nil {
		return false, nil
	}
	attempt.mu.Lock()
	ready := attempt.ready
	attempt.mu.Unlock()
	if !ready {
		return false, nil
	}
	if err := p.checkCommitEnvelopeV1(ctx, p.fresh); err != nil {
		return true, err
	}
	if p.slotSnapshot != nil {
		actual, ok := p.slotSnapshot.(rewindSlotNewSourceV2)
		if !ok {
			return true, rewindFailureV1("the reserved slot has no actual new-source handoff verifier")
		}
		if err := actual.ValidateNewColdSourceV2(ctx, p.operation, p.cold); err != nil {
			return true, err
		}
	} else if err := p.checkReadyCreationOutcomeV1(ctx, attempt); err != nil {
		return true, err
	}
	root, err := operation.LoadContext(ctx)
	if err != nil {
		return true, err
	}
	var selected *Manifest
	for _, member := range p.members {
		if member.id == p.selection.ID {
			selected, err = member.read(ctx, operation)
			break
		}
	}
	if err != nil || selected == nil {
		return true, errors.Join(snapshotChanged(), err)
	}
	info := publicationInfoFrom(selected, alive(selected.ID), root)
	if err := checkSecretPublicationResponse(info); err != nil {
		return true, err
	}
	if !p.rolledBack {
		p.result = &api.RewindResult{ID: p.selection.ID, Checkpoint: p.selection.Checkpoint, Session: info}
	}
	p.phase.Store(rewindCompletedV1)
	if p.journal != nil {
		if err := p.journal.phaseV1(ctx, "completed"); err != nil {
			return true, err
		}
		if err := p.journal.removeV1(ctx); err != nil {
			return true, err
		}
	}
	return true, nil
}
