//go:build linux

package host

import (
	"context"
	"time"
)

func (p *bridgeRewindPreparationV1) closeOwnedV1() error {
	if p == nil {
		return nil
	}
	if p.cancel != nil {
		p.cancel(context.Canceled)
	}
	if p.operation == nil {
		if p.stopClientWatch != nil {
			p.stopClientWatch()
		}
		if p.cancelExecution != nil {
			p.cancelExecution(context.Canceled)
		}
		return nil
	}
	if p.phase.Load() == rewindStoppingV1 && p.stopped == nil {
		stopped, err := p.warm.settleOriginalRewindStopV1(p.execution, p)
		if stopped == nil {
			return err
		}
		p.stopped = stopped
		p.phase.Store(rewindStoppedV1)
	}
	// After a genuine Stop the original action owns restoration. Use the
	// retained execution lifetime for cleanup, never a cancelled socket or
	// a journal-derived replacement Source.
	if p.stopped != nil && p.phase.Load() != rewindCompletedV1 {
		if p.cold == nil {
			if err := p.captureColdForRewindV1(p.execution); err != nil {
				return err
			}
		}
		if p.accounting == nil {
			if err := p.reserveRewindRestartV1(p.execution); err != nil {
				return err
			}
		}
		if p.phase.Load() < rewindMetadataCommittedV1 || p.phase.Load() == rewindRolledBackV1 {
			if err := p.rollbackRewindV1(p.execution); err != nil {
				return err
			}
		}
		if err := p.finishRewindRestartV1(p.execution); err != nil {
			return err
		}
	}
	if p.accounting != nil {
		ctx, cancel := context.WithTimeout(p.execution, 15*time.Second)
		err := p.accounting.Cancel(ctx, p.operation, p.cold)
		cancel()
		if err != nil {
			return err
		}
	}
	if p.disk != nil {
		if err := p.disk.Close(); err != nil {
			return err
		}
	}
	if p.metadataTxn != nil {
		if err := p.metadataTxn.closeV1(); err != nil {
			return err
		}
	}
	if p.storage != nil {
		if err := p.storage.closeV1(); err != nil {
			return err
		}
	}
	if p.snapshot != nil {
		if err := p.snapshot.Close(); err != nil {
			return err
		}
		p.snapshot = nil
	}
	if p.slotSnapshot != nil {
		if err := p.slotSnapshot.Close(); err != nil {
			return err
		}
		p.slotSnapshot = nil
	}
	if p.readyWarm != nil {
		if err := p.readyWarm.Close(); err != nil {
			return err
		}
		p.readyWarm = nil
	}
	if p.readyUnexpected != nil {
		if err := p.readyUnexpected.Close(); err != nil {
			return err
		}
		p.readyUnexpected = nil
	}
	if p.cold != nil {
		if err := p.cold.Close(); err != nil {
			return err
		}
	}
	if p.retry != nil {
		if err := p.retry.Close(); err != nil {
			return err
		}
	}
	if p.warm != nil {
		if p.stopped == nil {
			if err := p.warm.Close(); err != nil {
				return err
			}
		} else {
			if err := p.stopped.Close(); err != nil {
				return err
			}
		}
	}
	if p.publication != nil {
		if err := p.publication.close(); err != nil {
			return err
		}
	}
	if p.journal != nil && !p.journal.removalDoneV1() {
		if p.phase.Load() != rewindCompletedV1 {
			return rewindFailureV1("the pending rewind must settle before its original journal owner closes")
		}
		if !p.journal.removed {
			if err := p.journal.phaseV1(p.execution, "completed"); err != nil {
				return err
			}
		}
		if err := p.journal.removeV1(p.execution); err != nil {
			return err
		}
	}
	for _, member := range p.members {
		if err := member.close(); err != nil {
			return err
		}
	}
	if err := p.operation.CloseWithError(); err != nil {
		return err
	}
	p.active.Store(false)
	if p.stopClientWatch != nil {
		p.stopClientWatch()
	}
	if p.startupDelivery != nil {
		p.startupDelivery.clearV1()
	}
	clearRewindStartV1(&p.fresh)
	clear(p.commitEncoded)
	p.commitEncoded = nil
	if p.cancelExecution != nil {
		p.cancelExecution(context.Canceled)
	}
	return nil
}
