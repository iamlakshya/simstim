//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func (p *bridgeRewindPreparationV1) openStoppedRewindStorageV1(ctx context.Context) (returned error) {
	if p == nil || p.operation == nil || p.stopped == nil {
		return rewindFailureV1("the stopped disk has no actual native owner")
	}
	if p.storage != nil {
		if p.storage.openErr == nil && p.backend != nil {
			return nil
		}
		if p.disk != nil {
			return errors.Join(rewindFailureV1("the original partial stopped backend must settle before it can be retried"), p.storage.openErr)
		}
		if err := p.storage.closeV1(); err != nil {
			return err
		}
		p.storage = nil
		p.backend = nil
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	storage := &rewindStoppedStorageV1{preparation: p}
	p.storage = storage
	defer func() { storage.openErr = returned }()
	if p.slotSnapshot != nil {
		backend, closeNative, err := p.slotSnapshot.OpenStoppedBackendV1(ctx, p.operation, p.stopped)
		storage.closeNative = closeNative
		if backend != nil {
			p.backend = backend
		}
		if backend == nil && err == nil {
			return rewindFailureV1("the original stopped slot factory returned no actual backend")
		}
		return err
	}
	var err error
	storage.root, err = p.operation.native.root.OpenRoot(".")
	if err != nil {
		return err
	}
	if err := p.operation.native.rootRef.CheckRoot(storage.root); err != nil {
		return err
	}
	storage.vm, err = cachefs.OpenPrivateChild(storage.root, "vm", false)
	if err != nil {
		return err
	}
	storage.originalDisk, err = cachefs.OpenFile(storage.vm, "disk.img", os.O_RDONLY, 0600)
	if err != nil {
		return err
	}
	root, err := p.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	need, err := vmneed.DecodeV1(root.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return err
	}
	info, err := storage.originalDisk.Stat()
	if err != nil || info.Size() != need.DiskMax {
		return errors.Join(rewindFailureV1("the actual stopped disk differs from its original complete size"), err)
	}
	storage.store, err = checkpoint.OpenOwned(ctx, storage.root, storage.vm, storage.originalDisk, func(ctx context.Context) error { return p.stopped.Check(ctx, p.operation) })
	if err != nil {
		return err
	}
	p.backend = checkpoint.NewLocalBackendV1(storage.store)
	if err := p.backend.Probe(ctx); err != nil {
		return err
	}
	return p.backend.CheckSpaceV1(ctx, need.DiskMax)
}

func (p *bridgeRewindPreparationV1) checkAccountingOriginV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1) error {
	if p == nil || ctx == nil || operation == nil || operation != p.operation || !p.active.Load() || !p.consumed || p.prepareRequest == 0 || p.commitRequest == 0 || len(p.commitEncoded) == 0 || p.bridge == nil || p.execution == nil || p.execution.Err() != nil || operation.rewindPreparation != p || !operation.Valid(operation.Primary()) {
		return rewindFailureV1("the original rewind accounting producer ended")
	}
	p.bridge.mu.Lock()
	service := p.bridge.rewindPreparationService
	p.bridge.mu.Unlock()
	if service == nil || service.plan.Load() != p {
		return rewindFailureV1("the actual rewind accounting owner is no longer registered")
	}
	if p.cold == nil {
		if cold != nil || p.stopped == nil {
			return rewindFailureV1("the actual stopped rewind source is absent")
		}
		return p.stopped.Check(ctx, operation)
	}
	if cold != p.cold || cold.operation != operation || cold.vacancy == nil {
		return rewindFailureV1("the original rewind cold source or same stopped custody changed")
	}
	if cold.vacancy.stopped != p.stopped {
		if cold.vacancy.rewindFailed == nil || p.retry != cold.vacancy.rewindFailed {
			return rewindFailureV1("the retry has no actual failed-creation custody")
		}
		if err := cold.vacancy.rewindFailed.checkV1(ctx, operation); err != nil {
			return err
		}
	}
	phase := p.phase.Load()
	if phase < rewindStoppedV1 {
		return rewindFailureV1("a live preparation cannot issue cold restart accounting")
	}
	if phase >= rewindDiskPreparedV1 && phase < rewindRestartingV1 {
		// This branch maintains only an already-held accounting charge during
		// the stopped disk transaction. It never grants boot or disk mutation.
		return p.stopped.Check(ctx, operation)
	}
	return cold.Check(ctx, operation)
}
