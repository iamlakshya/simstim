package checkpoint

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (prepared *localPreparedRestoreV1) Commit(ctx context.Context) error {
	if prepared == nil || prepared.store == nil {
		return failure("the actual prepared rewind owner is absent", nil)
	}
	s := prepared.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := prepared.checkV1(ctx); err != nil {
		return err
	}
	if prepared.journal.Phase == RestoreExchangedV1 {
		return nil
	}
	if prepared.journal.Phase != RestorePreparedV1 {
		return failure("the original rewind transaction cannot commit in its current phase", nil)
	}
	partName := prepared.replacement.name
	exchanged, exchangeErr := exchangeFilesV1(s.diskRoot, diskName, partName)
	if !exchanged {
		return failure("cannot exchange the original stopped disk and its prepared replacement", exchangeErr)
	}
	// Record the physical exchange BEFORE any fallible sync or owner check.
	// Both actual descriptors stay retained even when those checks fail.
	prepared.replacement.name = diskName
	prepared.journal.Original.Name = partName
	prepared.journal.Replacement.Name = diskName
	prepared.journal.Phase = RestoreExchangedV1
	s.disk = prepared.replacement.file
	s.diskRef = prepared.replacement.ref
	s.safetySource = cachefs.Reference{}
	if err := errors.Join(exchangeErr, syncRoot(s.diskRoot), prepared.checkV1(ctx)); err != nil {
		return failure("the rewind disk exchanged but its original owner must reconcile the final check", err)
	}
	return nil
}

func (prepared *localPreparedRestoreV1) Rollback(ctx context.Context) error {
	if prepared == nil || prepared.store == nil {
		return failure("the actual prepared rewind owner is absent", nil)
	}
	s := prepared.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if prepared.closed || prepared.journal.Phase == RestoreFinalizedV1 {
		return failure("the original rewind transaction cannot roll back after finalization", nil)
	}
	if prepared.journal.Phase == RestoreRolledBackV1 && s.pendingRestore == nil {
		return errors.Join(prepared.closeErr, syncRoot(s.diskRoot), s.checkOwned(ctx, prepared.checkStopped))
	}
	if s.pendingRestore != prepared {
		return failure("the rewind rollback belongs to another original transaction", nil)
	}
	var exchangeErr error
	if prepared.journal.Phase == RestoreExchangedV1 {
		if err := prepared.checkV1(ctx); err != nil {
			return err
		}
		partName := prepared.journal.Original.Name
		exchanged, err := exchangeFilesV1(s.diskRoot, diskName, partName)
		exchangeErr = err
		if !exchanged {
			return failure("cannot restore the same original stopped disk inode", err)
		}
		prepared.replacement.name = partName
		prepared.journal.Original.Name = diskName
		prepared.journal.Replacement.Name = partName
		prepared.journal.Phase = RestoreRolledBackV1
		s.disk = prepared.original
		s.diskRef = prepared.originalRef
	} else if prepared.journal.Phase == RestorePreparedV1 {
		if err := prepared.checkV1(ctx); err != nil {
			return err
		}
	} else if prepared.journal.Phase != RestoreRolledBackV1 {
		return failure("the original rewind transaction has an unknown rollback phase", nil)
	}
	if err := errors.Join(s.checkOwned(ctx, prepared.checkStopped), prepared.originalRef.CheckFile(s.diskRoot, diskName, prepared.original, 0o600), prepared.replacement.ref.CheckFile(s.diskRoot, prepared.replacement.name, prepared.replacement.file, 0o600)); err != nil {
		return failure("the original disk was preserved but rollback cleanup remains retained", errors.Join(exchangeErr, err))
	}
	if err := s.diskRoot.Remove(prepared.replacement.name); err != nil {
		return failure("cannot remove the same creator's unused rewind replacement", errors.Join(exchangeErr, err))
	}
	prepared.journal.Phase = RestoreRolledBackV1
	s.pendingRestore = nil
	closeErr := prepared.replacement.file.Close()
	prepared.closeErr = errors.Join(prepared.closeErr, closeErr)
	if err := errors.Join(exchangeErr, closeErr, syncRoot(s.diskRoot), s.checkOwned(ctx, prepared.checkStopped)); err != nil {
		return failure("the original disk is restored but rollback requires journal reconciliation", err)
	}
	return nil
}

func (prepared *localPreparedRestoreV1) Finalize(ctx context.Context) error {
	if prepared == nil || prepared.store == nil {
		return failure("the actual prepared rewind owner is absent", nil)
	}
	s := prepared.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if prepared.closed {
		return failure("the actual prepared rewind owner is closed", nil)
	}
	if prepared.journal.Phase == RestoreFinalizedV1 && s.pendingRestore == nil {
		return errors.Join(prepared.closeErr, syncRoot(s.diskRoot), s.checkOwned(ctx, prepared.checkStopped))
	}
	if prepared.journal.Phase != RestoreExchangedV1 {
		return failure("the rewind must commit before its original disk can be released", nil)
	}
	if err := prepared.checkV1(ctx); err != nil {
		return err
	}
	if err := s.diskRoot.Remove(prepared.journal.Original.Name); err != nil {
		return failure("cannot release the same original disk after the host commit", err)
	}
	// The host calls Finalize only after its journal and manifest CAS commit.
	// The Store now owns its own actual created canonical descriptor. A caller's
	// original borrowed descriptor remains the caller's to close after its join.
	prepared.journal.Phase = RestoreFinalizedV1
	s.pendingRestore = nil
	s.ownedDisk = prepared.replacement.file
	var closeErr error
	if prepared.originalOwned {
		closeErr = prepared.original.Close()
		prepared.closeErr = errors.Join(prepared.closeErr, closeErr)
	}
	if err := errors.Join(closeErr, syncRoot(s.diskRoot), s.checkOwned(ctx, prepared.checkStopped)); err != nil {
		return failure("the rewind disk finalized but its owner must reconcile the final check", err)
	}
	return nil
}

func (prepared *localPreparedRestoreV1) Close() error {
	if prepared == nil || prepared.store == nil {
		return nil
	}
	s := prepared.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if prepared.closed {
		return prepared.closeErr
	}
	if s.pendingRestore == prepared || prepared.journal.Phase != RestoreFinalizedV1 && prepared.journal.Phase != RestoreRolledBackV1 {
		return failure("the original rewind must commit or roll back before its disk custody closes", nil)
	}
	prepared.closed = true
	prepared.closeErr = errors.Join(prepared.closeErr, prepared.snapshot.Close())
	return prepared.closeErr
}
