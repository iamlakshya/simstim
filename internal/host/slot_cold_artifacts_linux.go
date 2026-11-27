//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/checkpoint"
)

// The actual reserved owner alone creates this delegate while its SAME
// genuine stopped source remains held. Its duplicates preserve that native
// producer; neither a vector nor a disk reference constructs the class.
type slotColdArtifactsV2 struct {
	retireTo        *slotColdArtifactsV2
	mu              sync.Mutex
	issuer          *slotColdArtifactsV2
	owner           *SlotSnapshotOwnerV1
	cold            *ColdResumeV1
	vacancy         *auxiliaryVacancyV1
	artifacts       *auxiliaryArtifactsV1
	roles           []*os.File
	supervisor      *os.File
	diskInfo        os.FileInfo
	dispatched      *startupLaunchV1
	closing, closed bool
	closeErr        error
}

func (owner *SlotSnapshotOwnerV1) CaptureStoppedColdV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1) (*ColdResumeV1, error) {
	done, err := owner.beginV1(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	owner.operations.Lock()
	defer owner.operations.Unlock()
	if err := owner.checkStoppedV2(ctx, operation, stopped); err != nil {
		return nil, err
	}
	if owner.cold != nil {
		return owner.cold, rewindFailureV1("the SAME reserved Cold constructor must settle before another capture")
	}
	cold := &ColdResumeV1{operation: operation}
	owner.cold = cold
	// Retain the actual partial vacancy before returning any producer error.
	cold.vacancy, err = captureAuxiliaryVacancyFromStoppedV1(ctx, operation, stopped)
	if err != nil {
		return cold, err
	}
	artifacts, err := owner.captureColdArtifactsV2(ctx, cold)
	if artifacts != nil {
		cold.artifacts = artifacts
	}
	if err != nil {
		return cold, err
	}
	return cold, cold.Check(ctx, operation)
}

func (owner *SlotSnapshotOwnerV1) captureColdArtifactsV2(ctx context.Context, cold *ColdResumeV1) (*auxiliaryArtifactsV1, error) {
	if cold == nil || cold.operation != owner.operation || cold.vacancy == nil {
		return nil, rewindFailureV1("the reserved artifact constructor has another actual stopped vacancy")
	}
	if cold.vacancy.stopped != owner.warm.owner {
		retry := owner.slotRetry
		if retry == nil || retry.origin != owner || retry.next != cold || retry.receipt == nil || cold.vacancy.rewindFailed != retry.receipt {
			return nil, rewindFailureV1("the slot artifact retry has no actual failed creator receipt")
		}
		if err := retry.receipt.checkV1(ctx, owner.operation); err != nil {
			return nil, err
		}
	}
	slot := &slotColdArtifactsV2{owner: owner, cold: cold, vacancy: cold.vacancy}
	slot.issuer = slot
	artifacts := &auxiliaryArtifactsV1{slot: slot, vacancy: cold.vacancy}
	slot.artifacts = artifacts
	// Publish this actual resource owner before native DUP or cache work.
	if cold.artifacts == nil {
		cold.artifacts = artifacts
	}
	for _, file := range append(append([]*os.File(nil), owner.roles...), owner.lease) {
		copy, err := duplicateSlotSnapshotFileV1(file)
		if copy != nil {
			slot.roles = append(slot.roles, copy)
		}
		if err != nil {
			return artifacts, err
		}
	}
	var err error
	slot.supervisor, err = duplicateSlotSnapshotFileV1(owner.supervisor)
	if err != nil {
		return artifacts, err
	}
	slot.diskInfo, err = slot.roles[9].Stat()
	if err != nil {
		return artifacts, err
	}
	result, err := captureAuxiliaryArtifactsOwnedV2(ctx, cold.vacancy, slot)
	if result != nil {
		artifacts = result
	}
	return artifacts, err
}

func (owner *SlotSnapshotOwnerV1) RefreshStoppedColdV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1, cold *ColdResumeV1, restore checkpoint.PreparedRestoreV1) error {
	done, err := owner.beginV1(ctx)
	if err != nil {
		return err
	}
	defer done()
	if cold == nil {
		return rewindFailureV1("the reserved refresh has no actual Cold owner")
	}
	cold.life.Lock()
	defer cold.life.Unlock()
	owner.operations.Lock()
	defer owner.operations.Unlock()
	if err := owner.checkStoppedV2(ctx, operation, stopped); err != nil {
		return err
	}
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if owner.cold != cold || cold.operation != operation || cold.closed || cold.bound || cold.handed || cold.artifacts == nil || cold.artifacts.slot == nil || cold.artifacts.slot.owner != owner {
		return rewindFailureV1("the actual reserved Cold source changed before disk refresh")
	}
	if restore == nil {
		original, err := cold.artifacts.disk.Stat()
		if err != nil {
			return err
		}
		actual, err := owner.roles[9].Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(original, actual) || actual.Size() != owner.need.DiskMax || !original.ModTime().Equal(actual.ModTime()) {
			return rewindFailureV1("cancellation changed the actual original stopped disk before any replacement")
		}
	}
	if restore != nil {
		actual, ok := restore.(*slotSnapshotPreparedV2)
		if !ok || actual == nil || actual.issuer != actual || actual.backend == nil || actual.backend.owner != owner || actual.backend.prepared != actual || !actual.settledV2() {
			return rewindFailureV1("the slot refresh has no SAME actual settled replacement creator")
		}
	}
	next, err := owner.captureColdArtifactsV2(ctx, cold)
	if next == nil {
		return err
	}
	if err != nil {
		owner.failedArtifacts = append(owner.failedArtifacts, next)
		return err
	}
	old := cold.artifacts
	cold.artifacts = next
	// Both actual owners stay reachable if retiring the old handles fails.
	if err := old.Close(); err != nil {
		owner.failedArtifacts = append(owner.failedArtifacts, old)
		return err
	}
	return cold.artifacts.CheckBeforeActivation(ctx)
}

func (slot *slotColdArtifactsV2) checkDiskV2(ctx context.Context, artifacts *auxiliaryArtifactsV1, mutable bool) error {
	if slot == nil || slot.issuer != slot || slot.owner == nil || slot.owner.issuer != slot.owner || slot.cold == nil || slot.artifacts != artifacts || artifacts == nil || artifacts.slot != slot || artifacts.vacancy != slot.vacancy || slot.cold.vacancy != slot.vacancy || slot.owner.cold != slot.cold || slot.vacancy == nil || slot.owner.warm == nil || slot.owner.warm.owner == nil || len(slot.roles) != 18 || slot.supervisor == nil || slot.diskInfo == nil || slot.closing || slot.closed || slot.closeErr != nil {
		return rewindFailureV1("the actual slot artifact delegate ended or was substituted")
	}
	if slot.vacancy.stopped != slot.owner.warm.owner {
		retry := slot.owner.slotRetry
		if retry == nil || retry.next != slot.cold || retry.receipt != slot.vacancy.rewindFailed {
			return rewindFailureV1("the slot artifact delegate lost its actual failed creation")
		}
		if err := slot.owner.checkColdRetrySourceV2(ctx, slot.cold); err != nil {
			return err
		}
	}
	if err := slot.checkOldClosureV2(); err != nil {
		return err
	}
	if artifacts.disk == nil {
		return rewindFailureV1("the reserved artifacts have no actual original disk handle")
	}
	actual, err := artifacts.disk.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(slot.diskInfo, actual) || !actual.Mode().IsRegular() || actual.Size() != artifacts.need.DiskMax || actual.Size() != slot.owner.need.DiskMax || !mutable && !actual.ModTime().Equal(slot.diskInfo.ModTime()) {
		return rewindFailureV1("the original reserved artifact disk changed its native role")
	}
	return ctx.Err()
}

func (slot *slotColdArtifactsV2) checkOldClosureV2() error {
	if slot == nil || len(slot.roles) != 18 {
		return rewindFailureV1("the original reserved artifact custody is incomplete")
	}
	for _, file := range []*os.File{slot.supervisor, slot.roles[0], slot.roles[1], slot.roles[14]} {
		if _, err := slotSnapshotPIDV1(file, false); err != nil {
			return err
		}
	}
	for _, file := range []*os.File{slot.roles[2], slot.roles[13]} {
		if err := slotSnapshotGroupV1(file); err != nil {
			return err
		}
		empty, err := originalStopCgroupEmptyV1(file)
		if err != nil || !empty {
			return errors.Join(rewindFailureV1("the original reserved artifact group has no whole native closure"), err)
		}
	}
	return slotReservationObservationV2(slot.roles[17])
}

func (slot *slotColdArtifactsV2) checkHandedV2(ctx context.Context, artifacts *auxiliaryArtifactsV1, cold *ColdResumeV1, launch *startupLaunchV1) error {
	if slot == nil || slot.owner == nil || cold == nil || artifacts == nil || launch == nil || slot.cold != cold || slot.owner.cold != cold || cold.artifacts != artifacts || cold.startup != launch || slot.dispatched != nil && slot.dispatched != launch || cold.creation == nil {
		return rewindFailureV1("the mutable slot disk has no SAME original native takeover")
	}
	// The enclosing existing Cold check has validated this actual new helper,
	// its code/namespace and native unit/memory boundary. The fixed reserved
	// root creator must still match this SAME held canonical disk before use.
	if _, err := slotSnapshotPIDV1(cold.creation, true); err != nil {
		return err
	}
	return slot.checkDiskV2(ctx, artifacts, true)
}
