package microvm

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// NativeBindingV1 is borrowed evidence from the engine's actual native VMM and
// host-side guest connection. Only a live engine-owned sandbox constructs it.
// It supplies no grant, original operation or start/stop authority by itself.
// Its zero value is invalid on every platform.
type NativeBindingV1 struct{ verifier nativeBindingVerifierV1 }

type nativeBindingVerifierV1 interface {
	checkNativeBinding(context.Context, net.Conn, vmneed.NeedV1) error
	sessionID() string
}

type nativeSnapshotDiskV1 interface {
	withSnapshotDiskV1(context.Context, net.Conn, vmneed.NeedV1, func(*os.Root, *os.Root, *os.File) error) error
}

type nativeCheckpointBackendV1 interface {
	withCheckpointBackendV1(context.Context, net.Conn, vmneed.NeedV1, func(checkpoint.BackendV1) error) error
}

// HasSlotCheckpointBackendV1 is routing information only. WithBackendV1
// separately checks the actual original native Source on every borrow.
func (b *NativeBindingV1) HasSlotCheckpointBackendV1() bool {
	if b == nil || b.verifier == nil {
		return false
	}
	_, known := b.verifier.(nativeCheckpointBackendV1)
	return known
}

// CheckpointLoanV1 is issued only inside a live original native binding's
// callback. Its borrowed fixed-role files must be sent on that producer's SAME
// authenticated outside request lane. A vector alone is never an outside
// checkpoint Source; its receiver separately proves the actual creator/OP.
type CheckpointLoanV1 interface {
	WithLoanFilesV1(context.Context, func([]*os.File) error) error
	Close() error
}

type nativeCheckpointLoanV1 interface {
	withCheckpointLoanV1(context.Context, net.Conn, vmneed.NeedV1, func(CheckpointLoanV1) error) error
}

// CheckpointReservationLoanV2 is a distinct actual original keeper-issued
// reservation. Its single native send has eighteen roles including the SAME
// existing EX OFD. The interface alone grants no outside Source authority.
type CheckpointReservationLoanV2 interface {
	WithLoanFilesV2(context.Context, func([]*os.File) error) error
	Close() error
}

type nativeCheckpointReservationV2 interface {
	withCheckpointReservationLoanV2(context.Context, net.Conn, vmneed.NeedV1, func(CheckpointReservationLoanV2) error) error
}

// WithCheckpointReservationLoanV2 invokes only this live original binding's
// actual keeper reservation producer. Legacy seventeen-role custody cannot
// enter this path or be re-labelled as a reserved outside Source.
func (b *NativeBindingV1) WithCheckpointReservationLoanV2(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, use func(CheckpointReservationLoanV2) error) error {
	if b == nil || b.verifier == nil || ctx == nil || use == nil {
		return fmt.Errorf("the original reserved checkpoint binding is absent")
	}
	owner, ok := b.verifier.(nativeCheckpointReservationV2)
	if !ok {
		return fmt.Errorf("the original runtime has no keeper-issued checkpoint reservation producer")
	}
	return owner.withCheckpointReservationLoanV2(ctx, connection, expected, use)
}

type nativeForkDiskV1 interface {
	copyForkDiskV1(context.Context, net.Conn, vmneed.NeedV1, *os.File) error
}

// CopyForkDiskV1 copies only this original slot's actual disk into a fresh
// descriptor created inside the actual child's retained reservation/OP. The
// caller proves that destination creator before and after this operation.
// The output is staged data; only a separate genuine cold-start producer may
// consume that SAME output descriptor as a child VM's admitted session disk.
func (b *NativeBindingV1) CopyForkDiskV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, target *os.File) error {
	if ctx == nil || b == nil || b.verifier == nil || target == nil {
		return fmt.Errorf("the fork copy has no actual original slot and child file")
	}
	owner, ok := b.verifier.(nativeForkDiskV1)
	if !ok {
		return fmt.Errorf("the original runtime has no separate-account fork disk producer")
	}
	return owner.copyForkDiskV1(ctx, connection, expected, target)
}

// WithCheckpointLoanV1 borrows a real original slot producer's custody for a
// single native loan. The engine checks its exact actual connection and full
// Need before and after the callback, then closes and joins its local custody.
func (b *NativeBindingV1) WithCheckpointLoanV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, borrow func(CheckpointLoanV1) error) error {
	if ctx == nil || b == nil || b.verifier == nil || borrow == nil {
		return fmt.Errorf("the checkpoint loan has no original native slot binding")
	}
	owner, ok := b.verifier.(nativeCheckpointLoanV1)
	if !ok {
		return fmt.Errorf("the original runtime has no separate-account checkpoint loan producer")
	}
	return owner.withCheckpointLoanV1(ctx, connection, expected, borrow)
}

// WithBackendV1 borrows a separate-account checkpoint backend from the SAME
// original native slot producer. Its root operations accept fixed roles and
// original kernel descriptors; no private login filesystem is fabricated.
// The callback may keep a selected SnapshotV1, whose independent read-only
// custody survives this borrow. It must not keep or use the backend itself.
func (b *NativeBindingV1) WithBackendV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, borrow func(checkpoint.BackendV1) error) error {
	if ctx == nil || b == nil || b.verifier == nil || borrow == nil {
		return fmt.Errorf("the checkpoint backend has no original native slot binding")
	}
	owner, ok := b.verifier.(nativeCheckpointBackendV1)
	if !ok {
		return fmt.Errorf("the original runtime has no separate-account checkpoint backend")
	}
	return owner.withCheckpointBackendV1(ctx, connection, expected, borrow)
}

// WithSnapshotDiskV1 borrows the SAME disk and directory descriptors that the
// original engine passed to its VMM. They remain owned by that engine and must
// not be closed or used outside the callback. A caller also needs its original
// session operation and guest freeze lease before reading a consistent copy.
// Neither this method nor its callback grants stop or replacement authority.
func (b *NativeBindingV1) WithSnapshotDiskV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, borrow func(*os.Root, *os.Root, *os.File) error) error {
	if ctx == nil || borrow == nil || b == nil || b.verifier == nil {
		return fmt.Errorf("the snapshot has no original native binding or borrower")
	}
	owner, ok := b.verifier.(nativeSnapshotDiskV1)
	if !ok {
		return fmt.Errorf("the original runtime has no retained native session disk producer")
	}
	return owner.withSnapshotDiskV1(ctx, connection, expected, borrow)
}

// SessionID is correlation data from this actual engine instance. Callers
// compare it with their protected operation before relying on Check.
func (b *NativeBindingV1) SessionID() string {
	if b == nil || b.verifier == nil {
		return ""
	}
	return b.verifier.sessionID()
}

// Check validates the exact Start-returned connection, complete original boot
// data and retained native lifetime. The issuer separately requires its actual
// owned OP, supervisor lease and fresh protected primary before using this fact.
func (b *NativeBindingV1) Check(ctx context.Context, connection net.Conn, expected vmneed.NeedV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil || b.verifier == nil || connection == nil {
		return fmt.Errorf("the VM has no engine-owned native binding")
	}
	return b.verifier.checkNativeBinding(ctx, connection, expected)
}

type ownedGuestLink struct{ net.Conn }
