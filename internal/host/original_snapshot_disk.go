package host

import (
	"context"
	"net"
	"os"
)

// WithSnapshotDiskV1 borrows only this original prepared VM's actual disk.
// It projects no authority from the caller's manifest or a public Need.
func (receipt *OriginalVMNeed) WithSnapshotDiskV1(ctx context.Context, connection net.Conn, run func(*os.Root, *os.Root, *os.File) error) error {
	if ctx == nil || run == nil || receipt == nil || OriginalVMNeedFromContext(ctx) != receipt {
		return vmNeedError("the snapshot has no original native runtime receipt")
	}
	if err := receipt.CheckRunningNativeV1(ctx, connection); err != nil {
		return err
	}
	receipt.prepareMu.Lock()
	binding, need := receipt.binding, receipt.need
	receipt.prepareMu.Unlock()
	if binding == nil || need == nil {
		return vmNeedError("the snapshot's original prepared disk owner ended")
	}
	return binding.WithSnapshotDiskV1(ctx, connection, *need, run)
}

// CopyForkDiskV1 keeps the slot's original privileged disk with its actual
// native producer. The destination is a fresh file from the genuine child
// operation; no root path or saved allocation chooses the source.
func (receipt *OriginalVMNeed) CopyForkDiskV1(ctx context.Context, connection net.Conn, target *os.File) error {
	if ctx == nil || receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || connection == nil || target == nil {
		return vmNeedError("the slot fork copy has no original receipt and destination")
	}
	if err := receipt.CheckRunningNativeV1(ctx, connection); err != nil {
		return err
	}
	receipt.prepareMu.Lock()
	binding, need := receipt.binding, receipt.need
	receipt.prepareMu.Unlock()
	if binding == nil || need == nil {
		return vmNeedError("the original slot fork disk binding ended")
	}
	return binding.CopyForkDiskV1(ctx, connection, *need, target)
}
