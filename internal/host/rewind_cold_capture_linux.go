//go:build linux

package host

import (
	"context"
	"errors"
)

// Only the genuine SAME stopped producer creates this incomplete local owner.
// Keeping it separate prevents an errored constructor from issuing restart
// accounting merely because it returned a nonnil actual resource owner.
func (p *bridgeRewindPreparationV1) captureLocalColdForRewindV1(ctx context.Context) error {
	if p == nil || ctx == nil || p.operation == nil || p.stopped == nil || p.slotSnapshot != nil || p.cold != nil {
		return rewindFailureV1("the local cold capture changed its original stopped producer")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	if p.coldCapture == nil {
		vacancy, err := captureAuxiliaryVacancyFromStoppedV1(ctx, p.operation, p.stopped)
		if vacancy != nil {
			// A genuine vacancy constructor may itself return an actual
			// cleanup owner with an error. Retain it before testing err.
			p.coldCapture = &ColdResumeV1{operation: p.operation, vacancy: vacancy}
			p.coldCaptureErr = err
		}
		if err != nil {
			return err
		}
		if p.coldCapture == nil {
			return rewindFailureV1("the actual stopped constructor returned no retained vacancy")
		}
	}
	pending := p.coldCapture
	if pending.operation != p.operation || pending.vacancy == nil || pending.vacancy.stopped != p.stopped || pending.closed || pending.bound || pending.handed || pending.custody != nil {
		return rewindFailureV1("the retained local cold capture changed its actual original vacancy")
	}
	if err := pending.vacancy.Check(ctx); err != nil {
		return err
	}
	if pending.artifacts != nil && p.coldCaptureErr != nil {
		// The actual producer returned this partial because cleanup was unknown.
		// A second constructor must not replace it or turn its first error into
		// permission. Keep the vacancy and all native resources reachable.
		return errors.Join(rewindFailureV1("the original failed artifact capture still owns unresolved cleanup"), p.coldCaptureErr)
	}
	if pending.artifacts == nil {
		artifacts, err := captureAuxiliaryArtifactsV1(ctx, pending.vacancy)
		pending.artifacts, p.coldCaptureErr = artifacts, err // Retain BEFORE err.
		if err != nil {
			return err
		}
	}
	if err := pending.Check(ctx, p.operation); err != nil {
		return err
	}
	// This handoff follows the real complete constructor and native Check.
	// The partial pointer/error never supplies a Source or admission receipt.
	p.cold, p.coldCapture, p.coldCaptureErr = pending, nil, nil
	return nil
}
