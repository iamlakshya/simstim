package host

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/files"
)

// CheckpointSourceV1 retains the SAME accepted primary operation and original
// native running VM. It grants read-copy only; it cannot stop or replace disks.
type CheckpointSourceV1 struct {
	mu         sync.Mutex
	operation  *SessionOperation
	original   context.Context
	receipt    *OriginalVMNeed
	connection net.Conn
	generation string
	closed     bool
	closeErr   error
}

func checkpointSourceErrorV1(message string) error {
	return UserError("sandbox_changed", message, "Use the original running microvm and retry the checkpoint.")
}

// CaptureCheckpointSourceV1 transfers operation on success only. A row or a
// copied Need cannot construct the original prepared native receipt.
func CaptureCheckpointSourceV1(ctx, original context.Context, operation *SessionOperation, exactConnection net.Conn) (*CheckpointSourceV1, error) {
	if ctx == nil || original == nil || operation == nil || exactConnection == nil {
		return nil, checkpointSourceErrorV1("the checkpoint has no original operation and native runtime")
	}
	receipt := OriginalVMNeedFromContext(original)
	if receipt == nil || receipt.boot == nil || operation.Primary() != receipt.boot.loan.Primary {
		return nil, checkpointSourceErrorV1("the checkpoint source differs from its original native boot")
	}
	source := &CheckpointSourceV1{operation: operation, original: original, receipt: receipt, connection: exactConnection}
	manifest, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	source.generation = manifest.Run.Generation
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	return source, nil
}

func (source *CheckpointSourceV1) check(ctx context.Context) error {
	if source == nil || source.closed || ctx == nil || ctx.Err() != nil || source.operation == nil || source.original == nil || source.original.Err() != nil || OriginalVMNeedFromContext(source.original) != source.receipt || !source.operation.Valid(source.operation.Primary()) {
		return checkpointSourceErrorV1("the original checkpoint operation ended")
	}
	if err := source.receipt.CheckRunningNativeV1(source.original, source.connection); err != nil {
		return err
	}
	manifest, err := source.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if manifest.Wall == nil || manifest.Wall.Level != "microvm" {
		return UserError("rewind_off", "checkpoints are off: only microvm sandboxes have a session disk", "Use a microvm on a filesystem whose FICLONE probe succeeds.")
	}
	if manifest.SandboxOf != "" || manifest.ID != source.operation.Primary() || manifest.Life == nil || manifest.Life.Phase != LifeRunning || manifest.Run.Generation == "" || manifest.Run.Generation != source.generation || manifest.Run.HolderPID != os.Getpid() {
		return checkpointSourceErrorV1("the checkpoint's original running source changed")
	}
	return nil
}

// WithStore holds the actual native disk borrow through all I/O and cleanup.
// The Store and borrowed roots must never escape this call.
func (source *CheckpointSourceV1) WithStore(ctx context.Context, run func(*checkpoint.Store, files.Space, int64) error) (returned error) {
	if source == nil || run == nil {
		return checkpointSourceErrorV1("the checkpoint has no original storage consumer")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if err := source.check(ctx); err != nil {
		return err
	}
	receipt := source.receipt
	receipt.prepareMu.Lock()
	binding, need := receipt.binding, receipt.need
	receipt.prepareMu.Unlock()
	if binding == nil || need == nil {
		return checkpointSourceErrorV1("the checkpoint's original disk binding ended")
	}
	return binding.WithSnapshotDiskV1(ctx, source.connection, *need, func(session, diskRoot *os.Root, disk *os.File) (failure error) {
		if err := source.operation.native.rootRef.CheckRoot(session); err != nil {
			return err
		}
		if err := source.check(ctx); err != nil {
			return err
		}
		store, err := checkpoint.OpenOwned(ctx, session, diskRoot, disk, source.check)
		if err != nil {
			return err
		}
		defer func() { failure = errors.Join(failure, store.Close()) }()
		space, err := files.SpaceAt(session)
		if err != nil {
			return err
		}
		if err := run(store, space, need.DiskMax); err != nil {
			return err
		}
		return source.check(ctx)
	})
}

func (source *CheckpointSourceV1) Close() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return source.closeErr
	}
	source.closed = true
	if source.operation == nil {
		source.closeErr = checkpointSourceErrorV1("the original checkpoint operation is absent")
		return source.closeErr
	}
	source.closeErr = source.operation.CloseWithError()
	return source.closeErr
}

// WithBackend keeps local private descriptors or the slot's actual root
// producer with its native owner. A selected read-only Snapshot may escape;
// the backend itself remains borrowed and cannot cross a runtime stop.
func (source *CheckpointSourceV1) WithBackend(ctx context.Context, run func(checkpoint.BackendV1, int64) error) (returned error) {
	if source == nil || run == nil {
		return checkpointSourceErrorV1("the checkpoint has no actual backend consumer")
	}
	source.mu.Lock()
	if err := source.check(ctx); err != nil {
		source.mu.Unlock()
		return err
	}
	receipt := source.receipt
	receipt.prepareMu.Lock()
	binding, need := receipt.binding, receipt.need
	receipt.prepareMu.Unlock()
	if binding == nil || need == nil {
		source.mu.Unlock()
		return checkpointSourceErrorV1("the actual checkpoint backend binding ended")
	}
	if binding.HasSlotCheckpointBackendV1() {
		defer source.mu.Unlock()
		return binding.WithBackendV1(ctx, source.connection, *need, func(backend checkpoint.BackendV1) error {
			if err := source.check(ctx); err != nil {
				return err
			}
			if err := run(backend, need.DiskMax); err != nil {
				return err
			}
			return source.check(ctx)
		})
	}
	source.mu.Unlock()
	return source.WithStore(ctx, func(store *checkpoint.Store, _ files.Space, diskMax int64) error {
		return run(checkpoint.NewLocalBackendV1(store), diskMax)
	})
}
