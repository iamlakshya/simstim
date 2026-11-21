//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"golang.org/x/sys/unix"
)

func (prepared *slotSnapshotPreparedV2) checkReplacementV2() error {
	if prepared == nil || prepared.issuer != prepared || prepared.backend == nil || prepared.backend.owner == nil || len(prepared.original) != 18 || len(prepared.read) != 2 || prepared.replacement == nil || prepared.replacementInfo == nil {
		return rewindFailureV1("the actual slot replacement has incomplete native custody")
	}
	info, err := prepared.replacement.Stat()
	if err != nil {
		return err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	old, err := prepared.original[9].Stat()
	if err != nil {
		return err
	}
	owner, okOwner := old.Sys().(*syscall.Stat_t)
	flags, flagErr := unix.FcntlInt(prepared.replacement.Fd(), unix.F_GETFL, 0)
	if !ok || !okOwner || native.Uid != owner.Uid || native.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o660 || info.Size() != prepared.backend.owner.need.DiskMax || !os.SameFile(info, prepared.replacementInfo) || flagErr != nil || flags&unix.O_ACCMODE != unix.O_RDWR || flags&unix.O_PATH != 0 {
		return errors.Join(rewindFailureV1("the actual creator's slot replacement changed its native file role"), flagErr)
	}
	return nil
}

func (prepared *slotSnapshotPreparedV2) acceptJournalV2(raw json.RawMessage, wanted string) error {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return rewindFailureV1("the actual root replacement did not return bounded transaction data")
	}
	var actual checkpoint.TransactionV1
	if !api.ExactJSONFields(raw, actual) || json.Unmarshal(raw, &actual) != nil {
		return rewindFailureV1("the actual root replacement returned another transaction schema")
	}
	expected := prepared.expected
	if actual.Version != 1 || actual.Generation != expected.Generation || actual.Original.Role != "original" || actual.Replacement.Role != "replacement" || !actual.Original.Reference.Known() || !actual.Replacement.Reference.Known() || actual.Original.Reference.Directory || actual.Replacement.Reference.Directory || actual.Original.Reference.Mode != 0o660 || actual.Replacement.Reference.Mode != 0o660 || actual.Original.Reference.Size != prepared.backend.owner.need.DiskMax || actual.Replacement.Reference.Size != prepared.backend.owner.need.DiskMax {
		return rewindFailureV1("the actual root transaction changed its native disk correlation")
	}
	if !sameSlotSnapshotEntryV2(actual.Selected, prepared.selected.entry) || actual.Safety.Time.UTC().Format(checkpoint.TimeLayout) != expected.Safety.Time.UTC().Format(checkpoint.TimeLayout) || !sameSlotSnapshotMetadataV2(actual.Safety.Metadata, expected.Safety.Metadata) {
		return rewindFailureV1("the actual root transaction changed its original selection or safety checkpoint")
	}
	part := ".rewind-" + expected.Generation + ".part"
	switch actual.Phase {
	case checkpoint.RestorePreparedV1, checkpoint.RestoreRolledBackV1:
		if actual.Original.Name != "disk.img" || actual.Replacement.Name != part {
			return rewindFailureV1("the actual root transaction changed its original and prepared file roles")
		}
	case checkpoint.RestoreExchangedV1, checkpoint.RestoreFinalizedV1:
		if actual.Original.Name != part || actual.Replacement.Name != "disk.img" {
			return rewindFailureV1("the actual root transaction changed its exchanged file roles")
		}
	default:
		return rewindFailureV1("the actual root transaction has an unknown physical phase")
	}
	previous := prepared.journal
	if previous.Version != 0 && (previous.Original.Reference.Identity != actual.Original.Reference.Identity || previous.Replacement.Reference.Identity != actual.Replacement.Reference.Identity || previous.Original.Reference.Size != actual.Original.Reference.Size || previous.Replacement.Reference.Size != actual.Replacement.Reference.Size || !previous.Original.Reference.Modified.Equal(actual.Original.Reference.Modified) || !previous.Replacement.Reference.Modified.Equal(actual.Replacement.Reference.Modified)) {
		return rewindFailureV1("the original root transaction substituted a retained native disk")
	}
	// The authenticated actual root result describes the physical operation.
	// Publish it BEFORE any fallible local file check or ACK error is returned.
	prepared.journal = actual
	if actual.Phase != wanted {
		return rewindFailureV1("the actual root disk phase needs reconciliation through its retained creator")
	}
	return nil
}

func sameSlotSnapshotEntryV2(a, b checkpoint.Entry) bool {
	return a.Time.Equal(b.Time) && sameSlotSnapshotMetadataV2(a.Metadata, b.Metadata)
}
func sameSlotSnapshotMetadataV2(a, b checkpoint.Metadata) bool {
	left, err := checkpoint.EncodeMetadataV1(a)
	if err != nil {
		return false
	}
	right, err := checkpoint.EncodeMetadataV1(b)
	return err == nil && bytes.Equal(left, right)
}

func (prepared *slotSnapshotPreparedV2) phaseV2(ctx context.Context, action, wanted string) error {
	if prepared == nil || prepared.backend == nil || prepared.backend.owner == nil || prepared.issuer != prepared {
		return rewindFailureV1("the actual slot replacement creator ended")
	}
	backend := prepared.backend
	owner := backend.owner
	owner.operations.Lock()
	defer owner.operations.Unlock()
	if prepared.closed || backend.prepared != prepared {
		return rewindFailureV1("the actual prepared slot owner is incomplete or ended")
	}
	if err := backend.checkV2(ctx, true); err != nil {
		return err
	}
	if !prepared.admitted {
		// Retry the SAME real constructor after a late cancelled transport. The
		// current native source and actual created file must still check; a bare
		// journal or substituted object cannot enter this path.
		if prepared.journal.Version != 1 {
			return rewindFailureV1("the original replacement creation remains unobserved")
		}
		if err := prepared.checkReplacementV2(); err != nil {
			return err
		}
		prepared.admitted = true
	}
	if wanted != checkpoint.RestoreRolledBackV1 {
		if err := prepared.checkReplacementV2(); err != nil {
			return err
		}
	}
	metadata, err := checkpoint.EncodeMetadataV1(prepared.expected.Safety.Metadata)
	if err != nil {
		return err
	}
	request := slotlaunch.CheckpointOperationV1{Session: backend.operation.Primary(), Action: action, Name: prepared.selected.entry.Time.Format(checkpoint.TimeLayout), Generation: prepared.expected.Generation, Safety: prepared.expected.Safety.Time.UTC().Format(checkpoint.TimeLayout), Metadata: metadata}
	response, callErr := backend.callV2(ctx, prepared.rolesV2(true), request, nil)
	journalErr := prepared.acceptJournalV2(response.CheckpointTransaction, wanted)
	if wanted == checkpoint.RestoreFinalizedV1 && prepared.journal.Phase == checkpoint.RestoreFinalizedV1 && journalErr == nil {
		// Only this SAME actual prepared root creator may replace the parent's
		// retained current-data handle. This is not an adopted journal reference.
		current, dupErr := duplicateSlotSnapshotFileV1(prepared.replacement)
		if dupErr != nil {
			return errors.Join(callErr, journalErr, dupErr)
		}
		previous := owner.roles[9]
		owner.roles[9] = current
		owner.roleInfo[9], err = current.Stat()
		closeErr := previous.Close()
		if closeErr != nil {
			owner.failedFiles = append(owner.failedFiles, previous)
			owner.closeErr = errors.Join(owner.closeErr, closeErr)
		}
		return errors.Join(callErr, journalErr, err, closeErr, backend.checkV2(ctx, true))
	}
	return errors.Join(callErr, journalErr, backend.checkV2(ctx, true))
}

func (prepared *slotSnapshotPreparedV2) Commit(ctx context.Context) error {
	return prepared.phaseV2(ctx, "exchange", checkpoint.RestoreExchangedV1)
}
func (prepared *slotSnapshotPreparedV2) Rollback(ctx context.Context) error {
	return prepared.phaseV2(ctx, "rollback", checkpoint.RestoreRolledBackV1)
}
func (prepared *slotSnapshotPreparedV2) Finalize(ctx context.Context) error {
	return prepared.phaseV2(ctx, "finalize", checkpoint.RestoreFinalizedV1)
}

func (prepared *slotSnapshotPreparedV2) Close() error {
	if prepared == nil {
		return nil
	}
	if prepared.backend == nil || prepared.backend.owner == nil || prepared.issuer != prepared {
		return rewindFailureV1("a copied prepared slot owner cannot release actual custody")
	}
	owner := prepared.backend.owner
	owner.operations.Lock()
	defer owner.operations.Unlock()
	if prepared.closed {
		return prepared.closeErr
	}
	if prepared.creationAttempted && !prepared.settledV2() || prepared.backend.prepared != prepared || len(owner.pending) != 0 {
		return rewindFailureV1("the actual prepared slot transaction must settle before its native files close")
	}
	for _, file := range append(append(append([]*os.File(nil), prepared.original...), prepared.read...), prepared.replacement) {
		if file != nil {
			prepared.closeErr = errors.Join(prepared.closeErr, file.Close())
		}
	}
	prepared.closed = prepared.closeErr == nil
	if prepared.closed && !prepared.creationAttempted {
		prepared.backend.prepared = nil
	}
	return prepared.closeErr
}
