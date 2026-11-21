//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/fsbirth"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Exactly one genuine None-only subject lends cleanup of its own original
// fixed runtime directory. Neither this transaction nor DATA yields a runtime.
type noneDirectoryCleanupV1 struct {
	retirement                          nativeDescriptorRetirementsV1
	base                                *os.Root
	ready                               bool
	mu                                  sync.Mutex
	self                                *noneDirectoryCleanupV1
	operation                           *SessionOperation
	cold                                *NoneColdResumeV1
	deletion                            *noneDeleteSourceV1
	parent, root                        *os.Root
	record                              *noneDirectoryCreationV1
	created                             os.FileInfo
	started, unlinked, complete, closed bool
}

func (tx *noneDirectoryCleanupV1) mutationStartedV1() bool {
	if tx == nil || tx.self != tx {
		return false
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.started
}

func captureNoneDirectoryCleanupV1(ctx context.Context, operation *SessionOperation, cold *NoneColdResumeV1, deletion *noneDeleteSourceV1) (result *noneDirectoryCleanupV1, returned error) {
	if ctx == nil || operation == nil || (cold == nil) == (deletion == nil) {
		return nil, sessionLeaseError("the None directory cleanup has no exact actual subject")
	}
	if cold != nil {
		if err := cold.Check(ctx, operation); err != nil {
			return nil, err
		}
		if !cold.bound || operation.noneCold != cold || cold.handed {
			return nil, sessionLeaseError("the original None continuation has not accepted its real restart")
		}
	} else {
		if err := deletion.Check(ctx, operation); err != nil {
			return nil, err
		}
	}
	m, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !NoneSupervisorShapeV1(m) {
		return nil, sessionLeaseError("the directory cleanup subject changed its original None role")
	}
	tx := &noneDirectoryCleanupV1{operation: operation, cold: cold, deletion: deletion}
	tx.self = tx
	complete := false
	defer func() {
		if !complete {
			closeErr := tx.Close()
			returned = errors.Join(returned, tx.closeDiagnosticsV1())
			if closeErr != nil {
				result = tx
				returned = errors.Join(returned, closeErr)
			}
		}
	}()
	tx.base, err = cachefs.OpenPrivateRoot(paths.RuntimeDir(), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tx.parent, err = cachefs.OpenPrivateChild(tx.base, "g", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, err = tx.parent.Lstat(operation.Primary())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tx.record, err = decodeNoneDirectoryCreationV1(m)
	if err != nil {
		return nil, err
	}
	if !tx.record.ParentBirth.Known() || !tx.record.DirectoryBirth.Known() {
		return nil, noneDirectoryOriginErrorV1("this original None directory filesystem has no supported persistent generation; its path was preserved")
	}
	tx.root, err = cachefs.OpenPrivateChild(tx.parent, operation.Primary(), false)
	if err != nil {
		return nil, err
	}
	if err := tx.checkDirectoryV1(ctx); err != nil {
		return nil, err
	}
	tx.created, err = tx.root.Stat(".")
	if err != nil {
		return nil, err
	}
	tx.ready, complete = true, true
	return tx, nil
}

func (tx *noneDirectoryCleanupV1) checkDirectoryV1(ctx context.Context) error {
	if tx == nil || tx.self != tx || ctx == nil || tx.closed || tx.root == nil || tx.parent == nil || tx.record == nil {
		return sessionLeaseError("the actual None directory transaction ended or was copied")
	}
	if err := errors.Join(ctx.Err(), tx.record.Parent.CheckRoot(tx.parent), tx.record.Directory.CheckRoot(tx.root)); err != nil {
		return err
	}
	parentBirth, err := fsbirth.Capture(tx.parent)
	if err != nil {
		return err
	}
	directoryBirth, err := fsbirth.Capture(tx.root)
	if err != nil {
		return err
	}
	if !fsbirth.Same(parentBirth, tx.record.ParentBirth) || !fsbirth.Same(directoryBirth, tx.record.DirectoryBirth) {
		return noneDirectoryOriginErrorV1("the actual None runtime directory differs from its original filesystem generation")
	}
	named, err := tx.parent.Lstat(tx.operation.Primary())
	if err != nil {
		return err
	}
	actual, err := tx.root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(actual, named) {
		return noneDirectoryOriginErrorV1("the original None runtime name was replaced; the replacement was preserved")
	}
	return ctx.Err()
}

func (tx *noneDirectoryCleanupV1) checkRetainedSubjectV1(ctx context.Context) error {
	if tx == nil || tx.self != tx || tx.operation == nil || tx.operation.native == nil || tx.operation.native.borrowed || !tx.operation.Valid(tx.operation.Primary()) || (tx.cold == nil) == (tx.deletion == nil) {
		return sessionLeaseError("the original None directory lost its actual operation")
	}
	if tx.deletion != nil {
		return tx.deletion.Check(ctx, tx.operation)
	}
	return tx.cold.checkRetainedDirectorySourceV1(ctx)
}

// The ordinary closed producer uses only its own original holder and byte
// pins here. A managed producer must add its separate actual kernel-custody
// branch; it cannot acquire a replacement vacancy or use a data flag.
func (cold *NoneColdResumeV1) checkRetainedDirectorySourceV1(ctx context.Context) error {
	if cold == nil || cold.self != cold || ctx == nil {
		return sessionLeaseError("the original None directory continuation is absent")
	}
	cold.mu.Lock()
	defer cold.mu.Unlock()
	// This exact transaction was issued by cold.Check before mutation. After
	// accepted cleanup starts, client cancellation cannot manufacture another
	// subject or interrupt the retained physical unlink into a fake success.
	if cold.closed || !cold.bound || cold.handed || cold.operation == nil || cold.operation.noneCold != cold || cold.holder == nil || BootID() != cold.boot {
		return sessionLeaseError("the actual original None cleanup continuation ended or changed")
	}
	return errors.Join(CheckCaller(), cold.holder.check(ctx), cold.checkMetadataV1(ctx), cold.operation.native.check(ctx), ctx.Err())
}

func (tx *noneDirectoryCleanupV1) Remove(ctx context.Context) error {
	if tx == nil {
		return nil
	}
	if tx.self != tx || ctx == nil {
		return sessionLeaseError("the original None directory transaction is absent")
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return sessionLeaseError("the original None directory transaction closed")
	}
	if !tx.ready {
		return sessionLeaseError("the original None directory capture is incomplete and cannot remove storage")
	}
	if tx.complete {
		return nil
	}
	if !tx.started {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	owned := context.WithoutCancel(ctx)
	if err := tx.checkRetainedSubjectV1(owned); err != nil {
		return err
	}
	if tx.unlinked {
		return tx.finishUnlinkedV1()
	}
	if err := tx.checkDirectoryV1(owned); err != nil {
		return err
	}
	reader, err := tx.root.Open(".")
	if err != nil {
		return err
	}
	for {
		entries, readErr := reader.ReadDir(64)
		for _, entry := range entries {
			if err := tx.checkRetainedSubjectV1(owned); err != nil {
				reader.Close()
				return err
			}
			if err := tx.checkDirectoryV1(owned); err != nil {
				reader.Close()
				return err
			}
			tx.started = true
			if err := tx.root.RemoveAll(entry.Name()); err != nil {
				reader.Close()
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			reader.Close()
			return readErr
		}
	}
	if err := reader.Close(); err != nil {
		return err
	}
	if err := errors.Join(tx.checkRetainedSubjectV1(owned), tx.checkDirectoryV1(owned)); err != nil {
		return err
	}
	tx.started = true
	if err := tx.parent.Remove(tx.operation.Primary()); err != nil {
		return err
	}
	tx.unlinked = true
	return tx.finishUnlinkedV1()
}

func (tx *noneDirectoryCleanupV1) finishUnlinkedV1() error {
	info, err := tx.root.Stat(".")
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || tx.created == nil || !os.SameFile(info, tx.created) || stat.Nlink != 0 {
		return sessionLeaseError("the retained original None directory has no positive native unlink observation")
	}
	if err := syncCapturedDirectory(tx.parent); err != nil {
		return err
	}
	tx.complete = true
	return nil
}

func (tx *noneDirectoryCleanupV1) Close() error {
	if tx == nil {
		return nil
	}
	if tx.self != tx {
		return sessionLeaseError("a copied None directory transaction cannot close its original roots")
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return nil
	}
	if tx.started && !tx.complete {
		return noneDirectoryOriginErrorV1("the actual original None directory and parent remain retained after partial cleanup")
	}
	// tx.mu joins the only actual users of these original private roots.
	roots := []*os.Root{tx.root, tx.parent, tx.base}
	_ = tx.retirement.closeRootsV4(roots)
	unknown := tx.retirement.checkRootsV4(roots)
	if unknown == nil {
		tx.closed = true
	}
	return unknown
}

func (tx *noneDirectoryCleanupV1) closeDiagnosticsV1() error {
	if tx == nil || tx.self != tx {
		return nil
	}
	return tx.retirement.diagnosticsV4()
}

func (source *noneDeleteSourceV1) cleanupOriginalDirectoryV1(ctx context.Context) error {
	if source.directory == nil {
		tx, err := captureNoneDirectoryCleanupV1(ctx, source.operation, nil, source)
		if tx != nil {
			source.directory = tx
		}
		if err != nil {
			return err
		}
	}
	return source.directory.Remove(ctx)
}

func (cold *NoneColdResumeV1) cleanupPreviousNoneDirectoryV1(ctx context.Context) error {
	if cold == nil || cold.self != cold {
		return sessionLeaseError("the original None continuation is absent")
	}
	cold.mu.Lock()
	tx := cold.directoryCleanup
	cold.mu.Unlock()
	if tx == nil {
		var err error
		tx, err = captureNoneDirectoryCleanupV1(ctx, cold.operation, cold, nil)
		if tx != nil {
			cold.mu.Lock()
			cold.directoryCleanup = tx
			cold.mu.Unlock()
		}
		if err != nil {
			return err
		}
	}
	return tx.Remove(ctx)
}
