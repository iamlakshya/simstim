package host

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

// originalForkDiskDataV1 is a read-only DATA role below the genuine child's
// already-admitted OP. It grants no old slot Source, allocation or permission
// to create a VM. Only the actual new native boot/disk creator consumes it.
type originalForkDiskDataV1 struct {
	mu           sync.Mutex
	receipt      *OriginalVMNeed
	operation    *SessionOperation
	vm           *os.Root
	file         *os.File
	ref          cachefs.Reference
	used, closed bool
	closeErr     error
}

// The caller holds the original boot owner mutex while constructing receipt.
// ForkOf selects DATA only after the genuine accepted boot/OP was proved.
func captureOriginalForkDiskDataLockedV1(ctx context.Context, receipt *OriginalVMNeed, operation *SessionOperation, m *Manifest) error {
	if m == nil || !m.ForkPending || m.ForkOf == "" {
		return nil
	}
	if ctx == nil || receipt == nil || receipt.boot == nil || receipt.boot.owner == nil || receipt.boot.operation != operation || operation == nil || operation.originalBoot != receipt.boot || operation.native == nil || !operation.Valid(m.ID) || receipt.need == nil || !api.ValidSessionID(m.ForkOf) || m.ID == m.ForkOf || m.SandboxOf != "" || receipt.boot.ready || receipt.prepareUsed || receipt.prepareConfirmed {
		return vmNeedError("fork disk DATA has no genuine admitted new boot and same child operation")
	}
	vm, err := cachefs.OpenPrivateChild(operation.native.root, "vm", false)
	if err != nil {
		return err
	}
	file, err := cachefs.OpenFile(vm, "disk.img", os.O_RDONLY, 0600)
	if err != nil {
		return errors.Join(err, vm.Close())
	}
	ref, err := cachefs.CaptureFile(vm, "disk.img", file, 0600)
	if err != nil {
		return errors.Join(err, file.Close(), vm.Close())
	}
	if ref.Size != receipt.need.DiskMax {
		return errors.Join(vmNeedError("the staged fork disk DATA differs from the child's actual admitted size"), file.Close(), vm.Close())
	}
	source := &originalForkDiskDataV1{receipt: receipt, operation: operation, vm: vm, file: file, ref: ref}
	receipt.forkDiskData = source
	context.AfterFunc(ctx, func() { _ = source.closeV1() })
	return nil
}

// withOriginalForkDiskDataV1 is installed only by the closed host microvm
// constructors. A Spec, path, ForkOf flag or callback cannot issue a receipt.
func withOriginalForkDiskDataV1(ctx context.Context, consume func(*os.File) error) (provided bool, returned error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil || consume == nil {
		return false, vmNeedError("the fork disk DATA consumer has no original native boot receipt")
	}
	source := receipt.forkDiskData
	if source == nil {
		return false, nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed || source.used || source.receipt != receipt || source.operation == nil || receipt.boot == nil || receipt.boot.operation != source.operation || source.operation.originalBoot != receipt.boot || source.operation.native == nil || !source.operation.Valid(receipt.boot.loan.Primary) || source.vm == nil || source.file == nil {
		return true, vmNeedError("the original staged disk DATA was consumed or its native boot ended")
	}
	receipt.prepareMu.Lock()
	beforePrepare := !receipt.prepareUsed && !receipt.prepareConfirmed && receipt.binding == nil
	receipt.prepareMu.Unlock()
	if !beforePrepare {
		return true, vmNeedError("staged disk DATA cannot be borrowed after native VM preparation")
	}
	source.used = true
	returned = source.operation.withCurrentRead(ctx, func(owned context.Context) error {
		if err := errors.Join(owned.Err(), source.ref.CheckFile(source.vm, "disk.img", source.file, 0600)); err != nil {
			return err
		}
		if err := consume(source.file); err != nil {
			return err
		}
		return errors.Join(owned.Err(), source.ref.CheckFile(source.vm, "disk.img", source.file, 0600))
	})
	return true, returned
}

func (source *originalForkDiskDataV1) closeV1() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closeErr != nil {
		return source.closeErr
	}
	if source.closed {
		return nil
	}
	if source.file != nil {
		source.closeErr = source.file.Close()
	}
	if source.vm != nil {
		source.closeErr = errors.Join(source.closeErr, source.vm.Close())
	}
	if source.closeErr == nil {
		source.closed = true
	}
	return source.closeErr
}

// CloseOriginalForkDiskDataV1 closes only the private read role created by
// VMRunContextLocked. Cleanup may use its cancelled original context; it
// grants no Source, file selection or runtime action.
func CloseOriginalForkDiskDataV1(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	receipt, _ := ctx.Value(originalBootContextKeyV1{}).(*OriginalVMNeed)
	if receipt == nil || receipt.forkDiskData == nil {
		return nil
	}
	if receipt.forkDiskData.receipt != receipt {
		return vmNeedError("fork disk DATA cleanup lost its actual original receipt")
	}
	if err := receipt.forkDiskData.closeV1(); err != nil {
		return vmNeedError("the original fork disk DATA read role did not close: " + err.Error())
	}
	return nil
}
