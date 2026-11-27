//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

// This object is created only by the actual stopped backend and selected
// read owner. Journal DATA cannot reconstruct its native operands.
type slotSnapshotPreparedV2 struct {
	issuer                              *slotSnapshotPreparedV2
	backend                             *slotSnapshotStoppedV2
	selected                            *slotSnapshotReadV1
	original                            []*os.File
	read                                []*os.File
	replacement                         *os.File
	replacementInfo                     os.FileInfo
	expected                            checkpoint.TransactionV1
	journal                             checkpoint.TransactionV1
	creationAttempted, admitted, closed bool
	closeErr                            error
}

func (backend *slotSnapshotStoppedV2) PrepareRestore(ctx context.Context, snapshot checkpoint.SnapshotV1, transaction checkpoint.TransactionV1, checkStopped func(context.Context) error) (checkpoint.PreparedRestoreV1, error) {
	if err := checkpoint.ValidatePrepareV1(transaction); err != nil {
		return nil, err
	}
	if backend == nil || backend.owner == nil || checkStopped == nil {
		return nil, rewindFailureV1("the slot replacement has no actual stopped backend")
	}
	selected, ok := snapshot.(*slotSnapshotReadV1)
	if !ok || selected == nil || selected.origin != backend.owner || selected.issuer != selected {
		return nil, rewindFailureV1("the replacement selection has another actual read-cap producer")
	}
	owner := backend.owner
	owner.operations.Lock()
	defer owner.operations.Unlock()
	if err := errors.Join(backend.checkV2(ctx, false), selected.CheckV1(ctx), checkStopped(ctx)); err != nil {
		return nil, err
	}
	if backend.prepared != nil {
		return nil, rewindFailureV1("the original slot already has a prepared replacement owner")
	}
	prepared := &slotSnapshotPreparedV2{backend: backend, selected: selected}
	prepared.issuer = prepared
	// Publish before duplicating any actual right or dispatching the root
	// creator, so partial construction and uncertain send stay reachable.
	backend.prepared = prepared
	raw, err := json.Marshal(transaction)
	if err != nil {
		return prepared, err
	}
	if err := json.Unmarshal(raw, &prepared.expected); err != nil {
		return prepared, err
	}
	selected.operations.Lock()
	for _, file := range []*os.File{selected.disk, selected.metadata} {
		copy, copyErr := duplicateSlotSnapshotFileV1(file)
		if copy != nil {
			prepared.read = append(prepared.read, copy)
		}
		if copyErr != nil {
			selected.operations.Unlock()
			return prepared, copyErr
		}
	}
	selected.operations.Unlock()
	for _, file := range backend.rolesV2() {
		copy, copyErr := duplicateSlotSnapshotFileV1(file)
		if copy != nil {
			prepared.original = append(prepared.original, copy)
		}
		if copyErr != nil {
			return prepared, copyErr
		}
	}
	metadata, err := checkpoint.EncodeMetadataV1(transaction.Safety.Metadata)
	if err != nil {
		return prepared, err
	}
	request := slotlaunch.CheckpointOperationV1{Session: backend.operation.Primary(), Action: "prepare", Name: selected.entry.Time.Format(checkpoint.TimeLayout), Generation: transaction.Generation, Safety: transaction.Safety.Time.UTC().Format(checkpoint.TimeLayout), Metadata: metadata}
	prepared.creationAttempted = true
	response, callErr := backend.callV2(ctx, prepared.rolesV2(false), request, func(files []*os.File) error {
		if len(files) != 1 {
			return rewindFailureV1("the root replacement creator returned another actual file vector")
		}
		file, err := duplicateSlotSnapshotFileV1(files[0])
		if file != nil {
			prepared.replacement = file
		}
		return err
	})
	// A valid root phase is published before later descriptor/sync checks.
	journalErr := prepared.acceptJournalV2(response.CheckpointTransaction, checkpoint.RestorePreparedV1)
	if prepared.replacement == nil {
		return prepared, errors.Join(callErr, journalErr, rewindFailureV1("the actual prepared replacement has no retained native file"))
	}
	info, infoErr := prepared.replacement.Stat()
	if infoErr == nil {
		prepared.replacementInfo = info
	}
	nativeErr := errors.Join(journalErr, infoErr, prepared.checkReplacementV2(), backend.checkV2(ctx, true), checkStopped(ctx))
	if nativeErr == nil {
		prepared.admitted = true
	}
	return prepared, errors.Join(callErr, nativeErr)
}

func (prepared *slotSnapshotPreparedV2) rolesV2(replacement bool) []*os.File {
	roles := append(append([]*os.File(nil), prepared.original...), prepared.read...)
	if replacement {
		roles = append(roles, prepared.replacement)
	}
	return roles
}

func (prepared *slotSnapshotPreparedV2) settledV2() bool {
	return prepared != nil && prepared.issuer == prepared && (prepared.journal.Phase == checkpoint.RestoreFinalizedV1 || prepared.journal.Phase == checkpoint.RestoreRolledBackV1)
}

func (prepared *slotSnapshotPreparedV2) Journal() checkpoint.TransactionV1 {
	if prepared == nil || prepared.backend == nil || prepared.backend.owner == nil {
		return checkpoint.TransactionV1{}
	}
	prepared.backend.owner.operations.Lock()
	defer prepared.backend.owner.operations.Unlock()
	if prepared.issuer != prepared {
		return checkpoint.TransactionV1{}
	}
	raw, err := json.Marshal(prepared.journal)
	if err != nil {
		return checkpoint.TransactionV1{}
	}
	var copy checkpoint.TransactionV1
	if json.Unmarshal(raw, &copy) != nil {
		return checkpoint.TransactionV1{}
	}
	return copy
}

var _ checkpoint.PreparedRestoreV1 = (*slotSnapshotPreparedV2)(nil)
