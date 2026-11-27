package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"reflect"
	"runtime"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// OriginalBootDomain returns this process's actual pinned host domain only
// while its original native boot receipt is live. A Domain is storage data;
// it grants neither execution nor a replacement boot selection.
func OriginalBootDomain(ctx context.Context) (cachefs.Domain, error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil {
		return cachefs.Domain{}, vmNeedError("the runtime store has no original native boot owner")
	}
	owner := receipt.boot.placement
	if owner == nil {
		return cachefs.Domain{}, vmNeedError("the runtime store lost its original admitted placement")
	}
	if err := owner.checkIdentity(); err != nil {
		return cachefs.Domain{}, err
	}
	return owner.domain(), nil
}

// PrepareCorrelation binds the actual Start-returned connection and opaque
// engine-owned native binding once. The binding supplies physical facts; only
// this receipt's original OP, native supervisor and fresh protected Need can
// authorise its one bounded prepare projection. No raw connection can do so.
func (receipt *OriginalVMNeed) PrepareCorrelation(ctx context.Context, binding *microvm.NativeBindingV1, connection net.Conn) (*proto.VMPrepareCorrelationV1, error) {
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || receipt.need == nil || binding == nil || connection == nil {
		return nil, vmNeedError("VM prepare has no original receipt and actual owned native binding")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	if receipt.prepareUsed {
		return nil, vmNeedError("this actual native VM prepare was already consumed")
	}
	receipt.prepareUsed = true
	owner := receipt.boot.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup != receipt.boot || owner.pending == nil || !owner.Valid(receipt.boot.loan.Primary) ||
		!receipt.boot.operation.Valid(receipt.boot.loan.Primary) || receipt.boot.ready {
		return nil, vmNeedError("the original native VM startup scope ended before prepare")
	}
	if err := owner.pending.peer.Check(); err != nil {
		return nil, err
	}
	saved, err := receipt.boot.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(saved.VMNeedV1, receipt.rawNeed) || !bytes.Equal(receipt.boot.currentNeed, receipt.rawNeed) ||
		startupDigest(receipt.boot.need) != receipt.boot.loan.Need || !reflect.DeepEqual(saved.RuntimeManifest, receipt.boot.runtime) {
		return nil, vmNeedError("the protected original VM selection changed before prepare")
	}
	need, err := vmneed.DecodeV1(saved.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(*need, *receipt.need) || need.ValidateWall(runtime.GOARCH, wallLevel(saved), wallDriver(saved), saved.Limits.MemoryMax) != nil ||
		sessionMemoryMax(saved.Limits.MemoryMax) != saved.Limits.MemoryMax {
		return nil, vmNeedError("the original VM prepare conflicts with its admitted native selection")
	}
	if binding.SessionID() != receipt.boot.loan.Primary {
		return nil, vmNeedError("the actual native VM binding belongs to another protected operation")
	}
	if err := binding.Check(ctx, connection, *receipt.need); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	correlation := &proto.VMPrepareCorrelationV1{Version: 1, Session: receipt.boot.loan.Primary, Generation: owner.generation,
		NeedSHA256: startupDigest(receipt.rawNeed), Nonce: hex.EncodeToString(nonce[:])}
	receipt.binding, receipt.connection, receipt.correlation = binding, connection, *correlation
	return correlation, nil
}

// ConfirmPreparedCorrelation accepts one exact guest echo on the same actual
// Start connection, while the original native binding and OP are still live.
func (receipt *OriginalVMNeed) ConfirmPreparedCorrelation(ctx context.Context, connection net.Conn, echo *proto.VMPrepareCorrelationV1) error {
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || echo == nil {
		return vmNeedError("VM prepare has no original correlation receipt")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	if !receipt.prepareUsed || receipt.prepareConfirmed || receipt.binding == nil || receipt.connection == nil || *echo != receipt.correlation ||
		!receipt.boot.operation.Valid(receipt.boot.loan.Primary) {
		return vmNeedError("the guest prepare echo differs from this actual original VM connection")
	}
	if err := receipt.binding.Check(ctx, receipt.connection, *receipt.need); err != nil {
		return err
	}
	if err := receipt.binding.Check(ctx, connection, *receipt.need); err != nil {
		return err
	}
	if err := receipt.finishOriginalCheckpointReservationV2(ctx, connection); err != nil {
		return err
	}
	receipt.prepareConfirmed = true
	return nil
}
