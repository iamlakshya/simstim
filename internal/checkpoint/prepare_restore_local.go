package checkpoint

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// localPreparedRestoreV1 is a creator-owned operation, not journal data. It
// retains both inodes while disk.img changes roles and never exposes an FD.
type localPreparedRestoreV1 struct {
	store         *Store
	snapshot      *localSnapshotV1
	checkStopped  func(context.Context) error
	original      *os.File
	originalRef   cachefs.Reference
	originalOwned bool
	replacement   *ownedFile
	journal       TransactionV1
	closed        bool
	closeErr      error
}

func (s *Store) prepareRestoreV1(ctx context.Context, selected *localSnapshotV1, transaction TransactionV1, checkStopped func(context.Context) error) (_ PreparedRestoreV1, returned error) {
	if err := ValidatePrepareV1(transaction); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := errors.Join(s.noPreparedRestoreV1(), s.checkOwned(ctx, checkStopped)); err != nil {
		return nil, err
	}
	selected.mu.Lock()
	if err := selected.checkAgainstV1(s); err != nil {
		selected.mu.Unlock()
		return nil, err
	}
	snapshot, err := retainLocalSnapshotV1(selected.session, selected.entries, selected.key, selected)
	selected.mu.Unlock()
	if err != nil {
		return nil, err
	}
	prepared := &localPreparedRestoreV1{store: s, snapshot: snapshot, checkStopped: checkStopped, original: s.disk, originalOwned: s.ownedDisk == s.disk}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, snapshot.Close())
			if prepared.replacement != nil {
				returned = errors.Join(returned, prepared.replacement.remove())
			}
		}
	}()
	originalInfo, err := cachefs.ValidateDescriptor(s.disk, 0o600)
	if err != nil {
		return nil, err
	}
	if originalInfo.Size() <= 0 || originalInfo.Size() != snapshot.diskRef.Size {
		return nil, failure("the selected disk differs from the original admitted disk size", nil)
	}
	// This safety checkpoint is made only after the distinct original native
	// stopped proof. Retention waits until the host completes its transaction.
	safety, err := s.create(ctx, transaction.Safety.Time, transaction.Safety.Metadata, 0, checkStopped)
	if err != nil {
		return nil, err
	}
	prepared.originalRef, err = cachefs.CaptureFile(s.diskRoot, diskName, s.disk, 0o600)
	if err != nil {
		return nil, err
	}
	prepared.replacement, err = createPart(s.diskRoot)
	if err != nil {
		return nil, err
	}
	if err := s.clone(prepared.replacement.file, snapshot.disk); err != nil {
		return nil, cloneFailure("cannot prepare the selected rewind disk", err)
	}
	if err := errors.Join(prepared.replacement.file.Sync(), snapshot.checkAgainstV1(s), s.checkOwned(ctx, checkStopped), prepared.originalRef.CheckFile(s.diskRoot, diskName, s.disk, 0o600)); err != nil {
		return nil, failure("cannot finish the prepared rewind clone", err)
	}
	prepared.replacement.ref, err = cachefs.CaptureFile(s.diskRoot, prepared.replacement.name, prepared.replacement.file, 0o600)
	if err != nil {
		return nil, err
	}
	prepared.journal = TransactionV1{
		Version:     1,
		Generation:  transaction.Generation,
		Phase:       RestorePreparedV1,
		Selected:    Entry{Time: snapshot.entry.Time, Metadata: cloneMetadata(snapshot.entry.Metadata)},
		Safety:      safety,
		Original:    DiskRoleV1{Role: "original", Name: diskName, Reference: prepared.originalRef},
		Replacement: DiskRoleV1{Role: "replacement", Name: prepared.replacement.name, Reference: prepared.replacement.ref},
	}
	if err := errors.Join(syncRoot(s.diskRoot), s.checkOwned(ctx, checkStopped)); err != nil {
		return nil, failure("cannot retain the prepared rewind disk transaction", err)
	}
	s.pendingRestore = prepared
	complete = true
	return prepared, nil
}

func (prepared *localPreparedRestoreV1) Journal() TransactionV1 {
	if prepared == nil || prepared.store == nil {
		return TransactionV1{}
	}
	prepared.store.mu.Lock()
	defer prepared.store.mu.Unlock()
	return copyTransactionV1(prepared.journal)
}

func (prepared *localPreparedRestoreV1) checkV1(ctx context.Context) error {
	if prepared == nil || prepared.store == nil || prepared.closed || prepared.store.pendingRestore != prepared || ctx == nil || prepared.checkStopped == nil {
		return failure("the actual prepared rewind owner is unavailable", nil)
	}
	s := prepared.store
	if err := errors.Join(s.checkOwned(ctx, prepared.checkStopped), prepared.snapshot.checkAgainstV1(s)); err != nil {
		return err
	}
	if prepared.journal.Phase == RestorePreparedV1 {
		return errors.Join(prepared.originalRef.CheckFile(s.diskRoot, diskName, prepared.original, 0o600), prepared.replacement.ref.CheckFile(s.diskRoot, prepared.replacement.name, prepared.replacement.file, 0o600))
	}
	if prepared.journal.Phase == RestoreExchangedV1 {
		return errors.Join(prepared.originalRef.CheckFile(s.diskRoot, prepared.journal.Original.Name, prepared.original, 0o600), prepared.replacement.ref.CheckFile(s.diskRoot, diskName, prepared.replacement.file, 0o600))
	}
	return failure("the prepared rewind already settled its physical disk roles", nil)
}

var _ PreparedRestoreV1 = (*localPreparedRestoreV1)(nil)
